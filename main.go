package main

import (
	"bufio"
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
	"strconv"
	"strings"
	"time"
)

var (
	conf         *config
	httpClient   http.Client
	cpuDelay     = 1 * time.Second
	libratoDelay = 2 * time.Second
	hostname     string
)

func main() {
	var confFlag = flag.String("conf", "grotto.conf", "the config file")
	var err error
	flag.Parse()

	conf, err = readConfig(*confFlag)
	if err != nil {
		fmt.Printf("Could not read config file: %s", err)
		os.Exit(1)
	}

	hostname, err = os.Hostname()
	if err != nil {
		fmt.Printf("Could not read hostname: %s", err)
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
		Email string
		Token string
		Url   string
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
	return &conf, nil
}

// cpuStat holds values about cpu usage
type cpuStat struct {
	name   string
	user   int
	nice   int
	system int
	idle   int
	total  int
	epoch  int64
}

func (s *cpuStat) percentage(of int) float64 {
	return float64(of) / float64(s.total)
}

func (s *cpuStat) userPercentage() float64 {
	return s.percentage(s.user)
}

func (s *cpuStat) nicePercentage() float64 {
	return s.percentage(s.nice)
}

func (s *cpuStat) systemPercentage() float64 {
	return s.percentage(s.system)
}

func (s *cpuStat) idlePercentage() float64 {
	return s.percentage(s.idle)
}

// difference subtracts the values of one cpuStat from the receiver and returns
// a new struct
func (s *cpuStat) difference(other *cpuStat) cpuStat {
	return cpuStat{
		name:   other.name,
		user:   other.user - s.user,
		nice:   other.nice - s.nice,
		system: other.system - s.system,
		idle:   other.idle - s.idle,
		total:  other.total - s.total,
		epoch:  other.epoch,
	}
}

// gauge converts a cpuStat into a slice of gauges
func (s *cpuStat) metrics() []gauge {
	result := make([]gauge, 4)
	result[0] = gauge{Name: fmt.Sprintf("%s-%s", s.name, "user"), MeasureTime: s.epoch, Value: s.userPercentage(), Source: hostname}
	result[1] = gauge{Name: fmt.Sprintf("%s-%s", s.name, "nice"), MeasureTime: s.epoch, Value: s.nicePercentage(), Source: hostname}
	result[2] = gauge{Name: fmt.Sprintf("%s-%s", s.name, "system"), MeasureTime: s.epoch, Value: s.systemPercentage(), Source: hostname}
	result[3] = gauge{Name: fmt.Sprintf("%s-%s", s.name, "idle"), MeasureTime: s.epoch, Value: s.idlePercentage(), Source: hostname}
	return result
}

// startMetricsSender starts the goroutine that will consume payloads
// and send them to Librato
func startMetricsSender() chan interface{} {
	metrics := make(chan interface{})
	go func() {
		// setup state
		timeout := time.After(libratoDelay)
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
				fmt.Printf("Sending %d metrics to librato\n", payload.size())
				// pack up and send it out
				go func(payload *libratoPayload) {
					if err := sendPayload(payload); err != nil {
						fmt.Printf("Could not send payload: %s\n", err)
					}
				}(payload)
				timeout = time.After(libratoDelay)
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

// monitorCpuUsage starts a goroutine and sends cpuStats to a channel
// each successive cpuStat for a particular cpu will only consider values
// since the last measurement.
func monitorCpuUsage(metrics chan interface{}) {
	lookup := make(map[string]cpuStat)
	go func() {
		for {
			cpuStats, err := readCpuStats()
			if err != nil {
				fmt.Printf("Could not get cpu stats: %v\n", err)
			} else {
				for _, stat := range cpuStats {
					cumulative, ok := lookup[stat.name]
					if !ok {
						cumulative = *new(cpuStat)
						lookup[stat.name] = cumulative
						// skip this one
						continue
					}
					difference := cumulative.difference(&stat)
					for _, metric := range difference.metrics() {
						metrics <- metric
					}
					lookup[stat.name] = stat
				}
			}
			time.Sleep(cpuDelay)
		}
	}()
}

// readCpuStats reads /proc/stat, parses the values for the individual cpus
// and then returns a slice of cpuStat, one for each cpu
func readCpuStats() ([]cpuStat, error) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			panic(err)
		}
	}()
	stats := make([]cpuStat, 0)
	scanner := bufio.NewScanner(bufio.NewReader(file))
	for scanner.Scan() {
		text := scanner.Text()
		tokens := strings.Split(text, " ")
		cpuName := tokens[0]
		if strings.HasPrefix(cpuName, "cpu") {
			var stat cpuStat
			stat.name = cpuName
			stat.epoch = time.Now().Unix()
			for index, valueString := range tokens[1:] {
				value, err := atoi(valueString)
				if err != nil {
					return nil, err
				}
				switch index {
				case 0:
					stat.user = value
				case 1:
					stat.nice = value
				case 2:
					stat.system = value
				case 3:
					stat.idle = value
				}
				stat.total = stat.total + value
			}

			stats = append(stats, stat)
		}
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

// atoi just is a proxy for strconv.Atoi, but it also returns a helpful error message
func atoi(str string) (int, error) {
	value, err := strconv.Atoi(str)
	if err != nil {
		return value, fmt.Errorf("Could not parse %s to int", str)
	}
	return value, nil
}
