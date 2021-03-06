package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/netzsinus/defluxio-software"
	"github.com/tarm/goserial"
	"io"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var simulationMode = flag.Bool("sim", false, "simulation mode (does not need measurement hardware")
var configFile = flag.String("config", "defluxio-provider.conf", "configuration file")
var mkConfigFile = flag.Bool("genconfig", false, "generate an example configuration file")
var Cfg *defluxio.ProviderConfiguration
var readingChannel = make(chan float64)
var extract_wg sync.WaitGroup
var pusher_wg sync.WaitGroup

type ErrorMessage struct {
	Id      string
	Message string
}

func serial_read_readings(r io.Reader) {
	defer extract_wg.Done()
	lastfrequency := 0.0
	scanner := bufio.NewScanner(r)
	// Drop everything that is currently in the serial connection
	// buffer. Use a startup flag to signal this.
	startup := true
	timer := time.NewTimer(time.Second * 2)
	go func() {
		<-timer.C
		startup = false
	}()
	for scanner.Scan() {
		line := scanner.Text()
		if err := scanner.Err(); err != nil {
			log.Println("Failed to scan line:" + err.Error())
			continue
		}
		elements := strings.Split(line, ";")
		if elements[0] == "F" {
			frequency, err := strconv.ParseFloat(elements[1], 32)
			if err != nil {
				log.Println("Received broken frequency: " + line)
				continue
			}
			if frequency < 48 || frequency > 52 {
				log.Println("Frequency out of plausible range: " + line)
				continue
			}
			if startup {
				log.Println("Startup: Ignoring measurement", frequency)
				lastfrequency = frequency
			} else {
				// Now, we know that the measurement is a plausible one. Do a
				// last check for spikes (this is a strange phenomenon,
				// see https://github.com/gonium/defluxio/issues/8)
				if math.Abs(lastfrequency-frequency) < Cfg.Validation.SpikeThreshold {
					readingChannel <- frequency
				} else {
					log.Println("Rejected spike:", frequency)
				}
				lastfrequency = frequency
			}
		} else if elements[0] == "I" {
			log.Println("Info message: " + line)
		} else {
			log.Println("Received unknown data: " + line)
		}
	}
}

func simulate_readings() {
	defer extract_wg.Done()
	ticker := time.NewTicker(time.Second * 2)
	for {
		readingChannel <- 49.95 + (0.1 * rand.Float64())
		<-ticker.C
	}
}

func pusher() {
	defer pusher_wg.Done()
	reqUrl := fmt.Sprintf("%s:%d/api/submit/%s",
		Cfg.Network.Host,
		Cfg.Network.Port,
		Cfg.Meter.ID)
	for frequency := range readingChannel {
		log.Println("Frequency: " + strconv.FormatFloat(frequency, 'f', 5, 32))
		client := &http.Client{}
		body := defluxio.Reading{time.Now(), frequency}
		bodyBytes, _ := json.Marshal(body)
		//log.Println("Posting ", string(bodyBytes), " to endpoint ", reqUrl)
		req, err := http.NewRequest("POST", reqUrl, bytes.NewReader(bodyBytes))
		if err != nil {
			log.Fatal("Failed to compose submit request: ", err.Error())
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Add("X-API-Key", Cfg.Meter.Key)
		resp, err := client.Do(req)
		if err != nil {
			log.Println("Error posting data: ", err.Error())
			continue
		}

		response, rerr := ioutil.ReadAll(resp.Body)
		if rerr != nil {
			log.Println("Error getting post result data: ", err.Error())
			resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			decoder := json.NewDecoder(bytes.NewReader(response))
			var errorMessage ErrorMessage
			err := decoder.Decode(&errorMessage)
			if err != nil {
				log.Println("Failed to decode error message: " + err.Error() +
					", raw: " + string(response))
			} else {
				log.Println(resp.StatusCode, errorMessage.Id, ":", errorMessage.Message)
			}
		}
		resp.Body.Close()
	}
}

func init() {
	flag.Parse()
	if *mkConfigFile {
		log.Println("Creating default configuration in file " + *configFile)
		cfg := defluxio.MkDefaultProviderConfiguration()
		err := cfg.Save(*configFile)
		if err != nil {
			log.Fatal("Failed to create default configuration:", err.Error())
		}
		os.Exit(0)
	}
	var loaderror error
	Cfg, loaderror = defluxio.LoadProviderConfiguration(*configFile)
	if loaderror != nil {
		log.Fatal("Error loading configuration: ", loaderror.Error())
	}
}

func main() {
	if !*simulationMode {
		c := &serial.Config{Name: Cfg.Device.Path, Baud: Cfg.Device.Baudrate}
		s, err := serial.OpenPort(c)
		if err != nil {
			log.Fatal(err)
		}
		// just one reader, since it is a serial connection
		extract_wg.Add(1)
		go serial_read_readings(s)
	} else {
		rand.Seed(42)
		extract_wg.Add(1)
		go simulate_readings()
	}
	for c := 0; c < runtime.NumCPU(); c++ {
		pusher_wg.Add(1)
		go pusher()
	}
	extract_wg.Wait()
	close(readingChannel)
	pusher_wg.Wait()
}
