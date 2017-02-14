// Copyright 2017 Mesosphere. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Shopify/sarama"
	log "github.com/Sirupsen/logrus"
	"github.com/minio/minio-go"
	"net/http"
	"os"
	"sync"
)

const (
	onversion string = "0.1.0"
)

var (
	version bool
	wg      sync.WaitGroup
	// FQDN/IP + port of a Kafka broker:
	broker string
	// the data point ingestion queue:
	tqueue chan TrafficData
)

func init() {
	flag.BoolVar(&version, "version", false, "Display version information")
	flag.StringVar(&broker, "broker", "", "The FQDN or IP address and port of a Kafka broker. Example: broker-1.kafka.mesos:9382 or 10.0.3.178:9398")
	flag.Usage = func() {
		fmt.Printf("Usage: %s [args]\n\n", os.Args[0])
		fmt.Println("Arguments:")
		flag.PrintDefaults()
	}
	flag.Parse()

	// creating the buffered channel holding up to 10 traffic data points:
	tqueue = make(chan TrafficData, 10)
}

func servecontent() {
	fileServer := http.FileServer(http.Dir("content/"))
	http.Handle("/static/", http.StripPrefix("/static/", fileServer))
	http.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		t := <-tqueue
		log.WithFields(log.Fields{"func": "servecontent"}).Info(fmt.Sprintf("Serving data: %v records", len(t.Result.Records)))
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(t)
	})
	log.WithFields(log.Fields{"func": "servecontent"}).Info("Starting app server")
	http.ListenAndServe(":8080", nil)
}

func consume(topic string) {
	var consumer sarama.Consumer
	var partitionConsumer sarama.PartitionConsumer
	var err error
	defer wg.Done()
	if consumer, err = sarama.NewConsumer([]string{broker}, nil); err != nil {
		log.WithFields(log.Fields{"func": "consume"}).Error(err)
		return
	}
	defer func() {
		if err = consumer.Close(); err != nil {
			log.WithFields(log.Fields{"func": "consume"}).Error(err)
		}
	}()

	if partitionConsumer, err = consumer.ConsumePartition(topic, 0, sarama.OffsetNewest); err != nil {
		log.WithFields(log.Fields{"func": "consume"}).Error(err)
		return
	}

	defer func() {
		if err := partitionConsumer.Close(); err != nil {
			log.WithFields(log.Fields{"func": "consume"}).Error(err)
		}
	}()

	for {
		msg := <-partitionConsumer.Messages()
		traw := string(msg.Value)
		t := frommsg(traw)
		tqueue <- t
		log.WithFields(log.Fields{"func": "consume"}).Debug(fmt.Sprintf("%#v", t))
		log.WithFields(log.Fields{"func": "servecontent"}).Info("Received data from Kafka and queued it")
	}
}

func syncstaticdata() {
	endpoint := "34.250.247.12"
	accessKeyID, secretAccessKey := "F3QE89J9WPSC49CMKCCG", "2/parG/rllluCLMgHeJggJfY9Pje4Go8VqOWEqI9"
	useSSL := false
	bucket := "aarhus"
	object := "route_metrics_data.json"

	log.WithFields(log.Fields{"func": "syncstaticdata"}).Info(fmt.Sprintf("Trying to retrieve %s/%s from Minio", bucket, object))
	if mc, err := minio.New(endpoint, accessKeyID, secretAccessKey, useSSL); err != nil {
		log.WithFields(log.Fields{"func": "syncstaticdata"}).Fatal(fmt.Sprintf("%s ", err))
	} else {
		exists, err := mc.BucketExists(bucket)
		if err != nil || !exists {
			log.WithFields(log.Fields{"func": "syncstaticdata"}).Fatal(fmt.Sprintf("%s", err))
		} else {
			if err := mc.FGetObject(bucket, object, "./rmd.json"); err != nil {
				log.WithFields(log.Fields{"func": "syncstaticdata"}).Fatal(fmt.Sprintf("%s", err))
			} else {
				log.WithFields(log.Fields{"func": "syncstaticdata"}).Info(fmt.Sprintf("Retrieved route and metrics from Minio"))
				mtd := MetaTrafficData{}
				f, _ := os.Open("./rmd.json")
				defer os.Remove("./rmd.json")
				if err := json.NewDecoder(f).Decode(&mtd); err != nil {
					log.WithFields(log.Fields{"func": "frommsg"}).Fatal(err)
				}
				log.WithFields(log.Fields{"func": "syncstaticdata"}).Info(fmt.Sprintf("%+v", mtd))
			}
		}
	}
}

func main() {
	if version {
		about()
		os.Exit(0)
	}
	if broker == "" {
		flag.Usage()
		os.Exit(1)
	}

	// pull static route and metrics data from Minio:
	syncstaticdata()

	// serve static content (HTML page with OSM overlay) from /static
	// and traffic data in JSON from /data endpoint
	go servecontent()

	// kick of consuming data from Kafka:
	wg.Add(1)
	go consume("trafficdata")
	wg.Wait()
}