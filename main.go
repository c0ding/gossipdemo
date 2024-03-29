package main

import (
"encoding/json"
"flag"
"fmt"
"net/http"
"os"
"strings"
"sync"

"github.com/hashicorp/memberlist"
"github.com/pborman/uuid"
)

var (
	mtx        sync.RWMutex
	members    = flag.String("members", "", "comma seperated list of members")
	port       = flag.Int("port", 4001, "http port")
	items      = map[string]string{}
	broadcasts *memberlist.TransmitLimitedQueue
)

type broadcast struct {
	msg    []byte
	notify chan<- struct{}
}

type delegate struct{}

type update struct {
	Action string // add, del
	Data   map[string]string
}

func init() {
	flag.Parse()
}

func (b *broadcast) Invalidates(other memberlist.Broadcast) bool {
	return false
}

func (b *broadcast) Message() []byte {
	return b.msg
}

func (b *broadcast) Finished() {
	if b.notify != nil {
		close(b.notify)
	}
}

func (d *delegate) NodeMeta(limit int) []byte {
	return []byte{}
}

func (d *delegate) NotifyMsg(b []byte) {
	if len(b) == 0 {
		return
	}

	switch b[0] {
	case 'd': // data
		var updates []*update
		if err := json.Unmarshal(b[1:], &updates); err != nil {
			return
		}
		mtx.Lock()
		for _, u := range updates {
			for k, v := range u.Data {
				switch u.Action {
				case "add":
					items[k] = v
				case "del":
					delete(items, k)
				}
			}
		}
		mtx.Unlock()
	}
}

func (d *delegate) GetBroadcasts(overhead, limit int) [][]byte {
	return broadcasts.GetBroadcasts(overhead, limit)
}

func (d *delegate) LocalState(join bool) []byte {
	mtx.RLock()
	m := items
	mtx.RUnlock()
	b, _ := json.Marshal(m)
	return b
}

func (d *delegate) MergeRemoteState(buf []byte, join bool) {
	if len(buf) == 0 {
		return
	}
	if !join {
		return
	}
	var m map[string]string
	if err := json.Unmarshal(buf, &m); err != nil {
		return
	}
	mtx.Lock()
	for k, v := range m {
		items[k] = v
	}
	mtx.Unlock()
}

func addHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	key := r.Form.Get("key")
	val := r.Form.Get("val")
	mtx.Lock()
	items[key] = val
	mtx.Unlock()

	b, err := json.Marshal([]*update{
		&update{
			Action: "add",
			Data: map[string]string{
				key: val,
			},
		},
	})

	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	//广播数据
	broadcasts.QueueBroadcast(&broadcast{
		msg:    append([]byte("d"), b...),
		notify: nil,
	})
}

func delHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	key := r.Form.Get("key")
	mtx.Lock()
	delete(items, key)
	mtx.Unlock()

	b, err := json.Marshal([]*update{
		&update{
			Action: "del",
			Data: map[string]string{
				key: "",
			},
		},
	})

	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	broadcasts.QueueBroadcast(&broadcast{
		msg:    append([]byte("d"), b...),
		notify: nil,
	})
}

func getHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	key := r.Form.Get("key")
	mtx.RLock()
	val := items[key]
	mtx.RUnlock()
	w.Write([]byte(val))
}

func start() error {
	hostname, _ := os.Hostname()
	c := memberlist.DefaultLocalConfig()
	c.Delegate = &delegate{}
	c.BindPort = 0
	c.Name = hostname + "-" + uuid.NewUUID().String()
	//c.Events=c.Delegate
	//创建gossip网络
	m, err := memberlist.Create(c)
	if err != nil {
		return err
	}
	//第一个节点没有member，但从第二个开始就有member了
	if len(*members) > 0 {
		parts := strings.Split(*members, ",")
		_, err := m.Join(parts)
		if err != nil {
			return err
		}
	}
	broadcasts = &memberlist.TransmitLimitedQueue{
		NumNodes: func() int {
			return m.NumMembers()
		},
		RetransmitMult: 3,
	}
	node := m.LocalNode()
	fmt.Printf("Local member %s:%d\n", node.Addr, node.Port)
	return nil
}

func main() {
	if err := start(); err != nil {
		fmt.Println(err)
	}

	http.HandleFunc("/add", addHandler)
	http.HandleFunc("/del", delHandler)
	http.HandleFunc("/get", getHandler)
	fmt.Printf("Listening on :%d\n", *port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		fmt.Println(err)
	}
}


//
//import (
//	"flag"
//	"fmt"
//	"github.com/hashicorp/memberlist"
//	"io/ioutil"
//	"log"
//	"os"
//	"strconv"
//	"time"
//)
//type EventDelegate struct {
//}
//
//func (e *EventDelegate) NotifyUpdate(n *memberlist.Node) {
//	log.Println("Update", n.Addr.String(), n.Port)
//}
//
//func (e *EventDelegate) NotifyJoin(n *memberlist.Node) {
//	log.Println("join", n.Addr.String(), n.Port)
//}
//func (e *EventDelegate) NotifyLeave(n *memberlist.Node) {
//	log.Println("leave", n.Addr.String(), n.Port)
//}
//
//var bindPort = flag.Int("port", 8001, "gossip port")
//
//func main() {
//
//	flag.Parse()
//	hostname, _ := os.Hostname()
//	config := memberlist.DefaultLocalConfig()  // 返回一个 struct
//	fmt.Println(config)
//	config.Name = hostname + "-" + strconv.Itoa(*bindPort)
//	config.BindPort = *bindPort
//	config.AdvertisePort = *bindPort
//	// 配置里面加上，这样能捕获加入、离开、更新的事件。
//	config.Events = &EventDelegate{}
//	// 关闭默认日志
//	config.LogOutput = ioutil.Discard
//	fmt.Println("config.DisableTcpPings", config.DisableTcpPings)
//	fmt.Println("config.IndirectChecks", config.IndirectChecks)
//	fmt.Println("config.RetransmitMult", config.RetransmitMult)
//	fmt.Println("config.PushPullInterval", config.PushPullInterval)
//	fmt.Println("config.ProbeInterval", config.ProbeInterval)
//	fmt.Println("config.GossipInterval", config.GossipInterval)
//	fmt.Println("config.GossipNodes", config.GossipNodes)
//	fmt.Println("config.BindPort", config.BindPort)
//
//	list, _ := memberlist.Create(config)
//	list.Join([]string{"127.0.0.1:8001", "127.0.0.1:8002"})
//	fmt.Println(list.Members())
//
//	for {
//		fmt.Println("---------------start----------------")
//		for _, member := range list.Members() {
//			fmt.Printf("Member: %s %s\n", member.Name, member.Addr)
//		}
//		fmt.Println("---------------end----------------")
//		time.Sleep(time.Second * 3)
//	}
//
//}