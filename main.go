package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"time"
)

var (
	conf       *config
	httpClient http.Client
	hostname   string
)

func main() {
	var confFlag = flag.String("conf", "grotto.conf", "the config file")
	var err error
	flag.Parse()

	conf, err = readConfig(*confFlag)
	if err != nil {
		fmt.Printf("Could not read config file: %s\n", err)
		os.Exit(1)
	}

	hostname, err = os.Hostname()
	if err != nil {
		fmt.Printf("Could not read hostname: %s\n", err)
		os.Exit(1)
	}

	metrics := startMetricsSender()
	monitorCpuUsage(metrics)

	var quit chan bool
	<-quit
}

// the main struct we'll be sending to Librato
type libratoPayload struct {
	Gauges []gauge `json:"gauges"`
}

// libratoPayload adds a metric to its internal state. it returns an
// error if it does not know what to do with the metric
func (p *libratoPayload) addMetric(metric interface{}) error {
	switch metric := metric.(type) {
	default:
		return fmt.Errorf("Unsupported metric: %s", reflect.TypeOf(metric))
	case gauge:
		p.Gauges = append(p.Gauges, metric)
	}
	return nil
}

func (p *libratoPayload) size() int {
	return len(p.Gauges)
}

// a gauge is a one-off reading that is sent to Librato
type gauge struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	DisplayName string  `json:"display_name,omitempty"`
	MeasureTime int64   `json:"measure_time"` // epoch seconds
	Value       float64 `json:"value"`
	Source      string  `json:"source,omitempty"`
}

// the global config struct.
type config struct {
	Librato struct {
		Email         string
		Token         string
		Url           string
		PeriodSeconds int
	}
	Cpu struct {
		PeriodSeconds int
	}
}

// readConfig reads the global config for the agent and also checks to make
// sure required fields are present
func readConfig(loc string) (*config, error) {
	contents, err := ioutil.ReadFile(loc)
	if err != nil {
		return nil, err
	}
	var conf config
	err = json.Unmarshal(contents, &conf)
	if err != nil {
		return nil, err
	}
	if conf.Librato.Token == "" {
		return nil, errors.New("Missing an API token for Librato")
	}
	if conf.Librato.Email == "" {
		return nil, errors.New("Missing Email address for Librato")
	}
	if conf.Librato.Url == "" {
		return nil, errors.New("Missing Url for Librato")
	}
	if conf.Librato.PeriodSeconds <= 0 {
		fmt.Printf("Using default value of 5 for conf.Librato.PeriodSeconds\n")
		conf.Librato.PeriodSeconds = 5
	}
	if conf.Cpu.PeriodSeconds <= 0 {
		fmt.Printf("Using default value of 1 for conf.Cpu.PeriodSeconds\n")
		conf.Cpu.PeriodSeconds = 1
	}
	return &conf, nil
}

// startMetricsSender starts the goroutine that will consume payloads
// and send them to Librato
func startMetricsSender() chan interface{} {
	metrics := make(chan interface{})
	go func() {
		// setup state
		timeout := time.After(time.Duration(conf.Librato.PeriodSeconds) * time.Second)
		payload := new(libratoPayload)
		for {
			// gather up as many payloads as we can in libratoDelay.
			select {
			case metric := <-metrics:
				// sweet. put this metric into the payload
				if err := payload.addMetric(metric); err != nil {
					fmt.Printf("Could not add metric: %s\n", err)
				}
			case <-timeout:
				// pack up and send it out
				go func(payload *libratoPayload) {
					if err := sendPayload(payload); err != nil {
						fmt.Printf("Could not send payload: %s\n", err)
					}
				}(payload)
				timeout = time.After(time.Duration(conf.Librato.PeriodSeconds) * time.Second)
				payload = new(libratoPayload)
			}
		}
	}()
	return metrics
}

func sendPayload(payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	body := bytes.NewReader(data)
	req, err := http.NewRequest("POST", conf.Librato.Url, body)
	if err != nil {
		return err
	}
	credentials := fmt.Sprintf("%s:%s", conf.Librato.Email, conf.Librato.Token)
	authorization := fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(credentials)))
	req.Header.Add("Authorization", authorization)
	req.Header.Add("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("Librato responded with %d", resp.StatusCode)
	}
	return nil
}

// atoi just is a proxy for strconv.Atoi, but it also returns a helpful error message
func atoi(str string) (int, error) {
	value, err := strconv.Atoi(str)
	if err != nil {
		return value, fmt.Errorf("Could not parse %s to int", str)
	}
	return value, nil
}

// split splits a str based on separators of one or more whitespace tokens
var whitespaceRegexp = regexp.MustCompile("\\s+")

func split(str string) []string {
	return whitespaceRegexp.Split(str, -1)
}
