package tunnel

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dreamacro/clash/adapters"
	C "github.com/Dreamacro/clash/constant"
	"github.com/Dreamacro/clash/observable"
	R "github.com/Dreamacro/clash/rules"

	"gopkg.in/eapache/channels.v1"
)

var (
	tunnel *Tunnel
	once   sync.Once
)

type Tunnel struct {
	queue      *channels.InfiniteChannel
	rules      []C.Rule
	proxies    map[string]C.Proxy
	observable *observable.Observable
	logCh      chan interface{}
	configLock *sync.RWMutex
	traffic    *C.Traffic
}

func (t *Tunnel) Add(req C.ServerAdapter) {
	t.queue.In() <- req
}

func (t *Tunnel) Traffic() *C.Traffic {
	return t.traffic
}

func (t *Tunnel) Config() ([]C.Rule, map[string]C.Proxy) {
	t.configLock.RLock()
	defer t.configLock.RUnlock()
	return t.rules, t.proxies
}

func (t *Tunnel) Log() *observable.Observable {
	return t.observable
}

func (t *Tunnel) UpdateConfig() error {
	cfg, err := C.GetConfig()
	if err != nil {
		return err
	}

	proxies := make(map[string]C.Proxy)
	rules := []C.Rule{}

	proxysConfig := cfg.Section("Proxy")
	rulesConfig := cfg.Section("Rule")
	groupsConfig := cfg.Section("Proxy Group")

	// Parse proxies
	for _, key := range proxysConfig.Keys() {
		proxy := strings.Split(key.Value(), ",")
		if len(proxy) < 5 {
			continue
		}
		proxy = trimArr(proxy)
		if proxy[0] == "ss" {
			ssURL := fmt.Sprintf("ss://%s:%s@%s:%s", proxy[3], proxy[4], proxy[1], proxy[2])
			ss, err := adapters.NewShadowSocks(key.Name(), ssURL, t.traffic)
			if err != nil {
				return err
			}
			proxies[key.Name()] = ss
		}
	}

	// Parse rules
	for _, key := range rulesConfig.Keys() {
		rule := strings.Split(key.Name(), ",")
		if len(rule) < 3 {
			continue
		}
		rule = trimArr(rule)
		switch rule[0] {
		case "DOMAIN-SUFFIX":
			rules = append(rules, R.NewDomainSuffix(rule[1], rule[2]))
		case "DOMAIN-KEYWORD":
			rules = append(rules, R.NewDomainKeyword(rule[1], rule[2]))
		case "GEOIP":
			rules = append(rules, R.NewGEOIP(rule[1], rule[2]))
		case "IP-CIDR", "IP-CIDR6":
			rules = append(rules, R.NewIPCIDR(rule[1], rule[2]))
		case "FINAL":
			rules = append(rules, R.NewFinal(rule[2]))
		}
	}

	// Parse proxy groups
	for _, key := range groupsConfig.Keys() {
		group := strings.Split(key.Value(), ",")
		if len(group) < 4 {
			continue
		}
		group = trimArr(group)
		if group[0] == "url-test" {
			proxyNames := group[1 : len(group)-2]
			delay, _ := strconv.Atoi(group[len(group)-1])
			url := group[len(group)-2]
			var ps []C.Proxy
			for _, name := range proxyNames {
				if p, ok := proxies[name]; ok {
					ps = append(ps, p)
				}
			}

			adapter, err := adapters.NewURLTest(key.Name(), ps, url, time.Duration(delay)*time.Second)
			if err != nil {
				return fmt.Errorf("Config error: %s", err.Error())
			}
			proxies[key.Name()] = adapter
		}
	}

	// Add default proxies
	proxies["DIRECT"] = adapters.NewDirect(t.traffic)
	proxies["REJECT"] = adapters.NewReject()

	t.configLock.Lock()
	defer t.configLock.Unlock()

	// Stop existing URL tests
	for _, proxy := range t.proxies {
		if urlTest, ok := proxy.(*adapters.URLTest); ok {
			urlTest.Close()
		}
	}

	t.proxies = proxies
	t.rules = rules

	return nil
}

func (t *Tunnel) process() {
	queue := t.queue.Out()
	for {
		elm := <-queue
		conn := elm.(C.ServerAdapter)
		go t.handleConn(conn)
	}
}

func (t *Tunnel) handleConn(localConn C.ServerAdapter) {
	defer localConn.Close()
	addr := localConn.Addr()
	proxy := t.match(addr)
	remoteConn, err := proxy.Generator(addr)
	if err != nil {
		t.logCh <- newLog(WARNING, "Proxy connect error: %s", err.Error())
		return
	}
	defer remoteConn.Close()

	localConn.Connect(remoteConn)
}

func (t *Tunnel) match(addr *C.Addr) C.Proxy {
	t.configLock.RLock()
	defer t.configLock.RUnlock()

	for _, rule := range t.rules {
		if rule.IsMatch(addr) {
			if proxy, ok := t.proxies[rule.Adapter()]; ok {
				t.logCh <- newLog(INFO, "%v match %s using %s", addr.String(), rule.RuleType().String(), rule.Adapter())
				return proxy
			}
		}
	}
	t.logCh <- newLog(INFO, "%v doesn't match any rule using DIRECT", addr.String())
	return t.proxies["DIRECT"]
}

func newTunnel() *Tunnel {
	logCh := make(chan interface{})
	tunnel := &Tunnel{
		queue:      channels.NewInfiniteChannel(),
		proxies:    make(map[string]C.Proxy),
		observable: observable.NewObservable(logCh),
		logCh:      logCh,
		configLock: &sync.RWMutex{},
		traffic:    C.NewTraffic(time.Second),
	}
	go tunnel.process()
	go tunnel.subscribeLogs()
	return tunnel
}

func GetInstance() *Tunnel {
	once.Do(func() {
		tunnel = newTunnel()
	})
	return tunnel
}

// Helper function to trim spaces from array elements
func trimArr(arr []string) []string {
	for i := range arr {
		arr[i] = strings.TrimSpace(arr[i])
	}
	return arr
}
