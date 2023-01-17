package client

import (
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veertuinc/anka-prometheus-exporter/src/events"
	"github.com/veertuinc/anka-prometheus-exporter/src/log"
)

const (
	MAX_INTERVAL_SECONDS = 60
)

type Client struct {
	events              map[events.Event][]func(interface{}) error
	communicator        *Communicator
	timeoutSeconds      int64
	errorTimeoutSeconds int
	eventsMutex         sync.Mutex
}

func NewClient(addr, username, password string, interval int, certs TLSCerts) (*Client, error) {
	var log = log.GetLogger()

	communicator, err := NewCommunicator(addr, username, password, certs)
	if err != nil {
		return nil, err
	}

	c := &Client{
		events: map[events.Event][]func(interface{}) error{
			events.EVENT_NODE_UPDATED:               make([]func(interface{}) error, 0),
			events.EVENT_REGISTRY_DISK_DATA_UPDATED: make([]func(interface{}) error, 0),
			events.EVENT_VM_DATA_UPDATED:            make([]func(interface{}) error, 0),
			events.EVENT_REGISTRY_TEMPLATES_UPDATED: make([]func(interface{}) error, 0),
		},
		communicator:        communicator,
		timeoutSeconds:      int64(interval),
		errorTimeoutSeconds: 10,
	}
	if err := c.communicator.TestConnection(); err != nil {
		log.Fatal(err)
		return nil, err
	}
	return c, nil
}

func (this *Client) Init() {
	// We must first populate the data from the Controller API that is going to be stored in state before we attempt to create metrics from it
	// Order matters here since GetVmsData for example relies on RegistryTemplatesData
	this.communicator.GetRegistryTemplatesData()
	go this.initDataLoop(this.communicator.GetNodesData, events.EVENT_NODE_UPDATED)
	go this.initDataLoop(this.communicator.GetVmsData, events.EVENT_VM_DATA_UPDATED)
	go this.initDataLoop(this.communicator.GetRegistryDiskData, events.EVENT_REGISTRY_DISK_DATA_UPDATED)
	go this.initDataLoop(this.communicator.GetRegistryTemplatesData, events.EVENT_REGISTRY_TEMPLATES_UPDATED)
}

func (this *Client) Register(ev events.Event, eventHandler func(interface{}) error) error {
	this.eventsMutex.Lock()
	defer this.eventsMutex.Unlock()
	val, ok := this.events[ev]
	if ok {
		this.events[ev] = append(val, eventHandler)
	} else {
		return fmt.Errorf("no such event id: ", ev)
	}
	return nil
}

func (this *Client) UpdateInterval(i int64) {
	if i > 1 {
		if i > MAX_INTERVAL_SECONDS {
			atomic.StoreInt64(&this.timeoutSeconds, MAX_INTERVAL_SECONDS)
		} else {
			atomic.StoreInt64(&this.timeoutSeconds, i-1)
		}
	}
}

// Loops over each eventHandler inside of the metrics/metric_*.go files and populates the values for each metric
func (this *Client) initDataLoop(f func() (interface{}, error), ev events.Event) {
	var log = log.GetLogger()
	for {
		if log.GetLevel().String() == "debug" {
			log.Debugln("Requesting data for: " + runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name())
		}
		data, err := f()
		if err != nil {
			log.Errorf("could not get data: %+v", err)
			time.Sleep(time.Duration(this.errorTimeoutSeconds) * time.Second)
			continue
		}
		this.eventsMutex.Lock()
		events := this.events[ev]
		this.eventsMutex.Unlock()
		for _, eventHandler := range events {
			if err := eventHandler(data); err != nil {
				log.Errorf("ignoring event handler failure for event id %+v - Error: %+v", ev, err)
			}
		}
		time.Sleep(time.Duration(atomic.LoadInt64(&this.timeoutSeconds)) * time.Second)
	}
}
