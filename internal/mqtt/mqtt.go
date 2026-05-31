// Package mqtt publishes winnow meters to Home Assistant via MQTT Discovery.
// Each published meter becomes its own HA device with energy / power / signal
// sensors. BuildDiscovery is a pure function so the payload shape is unit-tested
// without a broker.
package mqtt

import (
	"encoding/json"
	"fmt"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"winnow/internal/config"
	"winnow/internal/model"
)

const availabilityTopic = "winnow/status"

// DiscoveryMsg is a retained config publish.
type DiscoveryMsg struct {
	Topic   string
	Payload []byte
}

func deviceID(id int64) string { return fmt.Sprintf("winnow_%d", id) }
func stateTopic(id int64, kind string) string {
	return fmt.Sprintf("winnow/%d/%s", id, kind)
}

// BuildDiscovery returns the retained discovery configs for a meter's sensors.
func BuildDiscovery(prefix string, m model.Meter) []DiscoveryMsg {
	dev := deviceID(m.EndpointID)
	name := dev
	if m.PubName != nil && *m.PubName != "" {
		name = *m.PubName
	}
	device := map[string]any{
		"identifiers":  []string{dev},
		"name":         name,
		"manufacturer": "winnow",
		"model":        m.Commodity + " meter (ERT)",
	}
	unit := ""
	if m.PubUnit != nil {
		unit = *m.PubUnit
	}

	type sensor struct {
		kind        string
		nameSuffix  string
		deviceClass string
		stateClass  string
		unit        string
	}
	sensors := []sensor{
		{"energy", "Energy", "energy", "total_increasing", unit},
		{"power", "Power", "power", "measurement", "W"},
		{"signal", "Signal", "", "measurement", "pkt/h"},
	}
	var out []DiscoveryMsg
	for _, s := range sensors {
		cfg := map[string]any{
			"name":               s.nameSuffix,
			"unique_id":          fmt.Sprintf("%s_%s", dev, s.kind),
			"object_id":          fmt.Sprintf("%s_%s", dev, s.kind),
			"state_topic":        stateTopic(m.EndpointID, s.kind),
			"availability_topic": availabilityTopic,
			"device":             device,
		}
		if s.deviceClass != "" {
			cfg["device_class"] = s.deviceClass
		}
		if s.stateClass != "" {
			cfg["state_class"] = s.stateClass
		}
		if s.unit != "" {
			cfg["unit_of_measurement"] = s.unit
		}
		payload, _ := json.Marshal(cfg)
		topic := fmt.Sprintf("%s/sensor/%s_%s/config", prefix, dev, s.kind)
		out = append(out, DiscoveryMsg{Topic: topic, Payload: payload})
	}
	return out
}

// Publisher manages the broker connection and publishes discovery + state.
type Publisher struct {
	client paho.Client
	prefix string
	known  map[int64]bool // meters we've already published discovery for
}

func NewPublisher() *Publisher { return &Publisher{known: map[int64]bool{}} }

// Connect (re)connects with the given config. Safe to call on config change.
func (p *Publisher) Connect(cfg config.Config) error {
	if p.client != nil && p.client.IsConnected() {
		p.client.Disconnect(250)
	}
	p.prefix = cfg.MQTTPrefix
	p.known = map[int64]bool{}
	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%d", cfg.MQTTHost, cfg.MQTTPort)).
		SetClientID("winnow-worker").
		SetUsername(cfg.MQTTUser).
		SetPassword(cfg.MQTTPass).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetWill(availabilityTopic, "offline", 1, true).
		SetOnConnectHandler(func(c paho.Client) {
			c.Publish(availabilityTopic, 1, true, "online")
		})
	p.client = paho.NewClient(opts)
	tok := p.client.Connect()
	if !tok.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("mqtt connect timeout")
	}
	return tok.Error()
}

func (p *Publisher) Connected() bool { return p.client != nil && p.client.IsConnected() }

// PublishState publishes a meter's latest values, registering discovery once.
func (p *Publisher) PublishState(m model.Meter, energy, power *float64, signal float64) {
	if p.client == nil {
		return
	}
	if !p.known[m.EndpointID] {
		for _, d := range BuildDiscovery(p.prefix, m) {
			p.client.Publish(d.Topic, 1, true, d.Payload)
		}
		p.known[m.EndpointID] = true
	}
	if energy != nil {
		p.client.Publish(stateTopic(m.EndpointID, "energy"), 0, false, fmt.Sprintf("%g", *energy))
	}
	if power != nil {
		p.client.Publish(stateTopic(m.EndpointID, "power"), 0, false, fmt.Sprintf("%g", *power))
	}
	p.client.Publish(stateTopic(m.EndpointID, "signal"), 0, false, fmt.Sprintf("%g", signal))
}

// Remove clears a meter's retained discovery configs (un-publish from HA).
func (p *Publisher) Remove(m model.Meter) {
	if p.client == nil {
		return
	}
	for _, d := range BuildDiscovery(p.prefix, m) {
		p.client.Publish(d.Topic, 1, true, []byte{})
	}
	delete(p.known, m.EndpointID)
}

func (p *Publisher) Close() {
	if p.client != nil && p.client.IsConnected() {
		p.client.Publish(availabilityTopic, 1, true, "offline")
		p.client.Disconnect(250)
	}
}
