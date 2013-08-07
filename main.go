package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	var confFlag = flag.String("conf", "grotto.conf", "the config file")
	flag.Parse()

	conf, err := readConfig(*confFlag)
	if err != nil {
		fmt.Printf("Could not read config file: %s", err)
		os.Exit(1)
	}
	fmt.Printf("%+v\n", conf)
	for {
		stats := monitorCpuUsage()
		for stat := range stats {
			fmt.Println(stat)
		}
	}
}

// the global config struct.
type config struct {
    Librato struct {
        Token string
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
}

func (s *cpuStat) String() string {
	var total float64 = float64(s.total)
	return fmt.Sprintf(
		"{%s User:%0.2f Nice:%0.2f System:%0.2f Idle:%0.2f}",
		s.name,
		float64(s.user*100)/total,
		float64(s.nice*100)/total,
		float64(s.system*100)/total,
		float64(s.idle*100)/total)
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
		total:  other.total - s.total}
}

// monitorCpuUsage starts a goroutine and sends cpuStats on a returned channel.
// each successive cpuStat for a particular cpu will only consider values
// since the last measurement.
func monitorCpuUsage() chan *cpuStat {
	lookup := make(map[string]cpuStat)
	ch := make(chan *cpuStat)
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
					ch <- &difference
					lookup[stat.name] = stat
				}
			}
			time.Sleep(1 * time.Second)
		}
	}()
	return ch
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
		if strings.HasPrefix(cpuName, "cpu") && len(cpuName) > 3 {
			var stat cpuStat
			stat.name = cpuName
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
