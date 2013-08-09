package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

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

func (s *cpuStat) usagePercentage() float64 {
	return s.percentage(s.user + s.nice + s.system)
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

// gauge converts a cpuStat into a slice of gauges
func (s *cpuStat) metrics() []gauge {
    newGauge := func(name string, value float64) gauge {
        return gauge{Name: fmt.Sprintf("%s-%s", s.name, name), MeasureTime: s.epoch, Value: value, Source: hostname}
    }
	return []gauge{
        newGauge("user", s.userPercentage()),
        newGauge("nice", s.nicePercentage()),
        newGauge("system", s.systemPercentage()),
        newGauge("idle", s.idlePercentage()),
        newGauge("usage", s.usagePercentage()),
	}
}

// difference subtracts the values of one cpuStat from the receiver and returns
// a new struct
func (s *cpuStat) difference(other *cpuStat) cpuStat {
	return cpuStat{
		name:   other.name,
		epoch:  other.epoch,
		user:   other.user - s.user,
		nice:   other.nice - s.nice,
		system: other.system - s.system,
		idle:   other.idle - s.idle,
		total:  other.total - s.total,
	}
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
			time.Sleep(time.Duration(conf.Cpu.PeriodSeconds) * time.Second)
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
		tokens := split(text)
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
