// (C) 2014 Mathias Dalheimer <md@gonium.net>. See LICENSE file for
// license.
package defluxio

import (
	"encoding/json"
	"fmt"
	"github.com/influxdata/influxdb/client/v2"
	"log"
	"time"
)

// TODO: Move MeterID out of MeterReading - too much duplication
// of the meter name. New type "MeterTimeseries?"
type MeterReading struct {
	MeterID string
	Reading Reading
}

// ByTimestamp implements sort.Interface for []MeterReading
// based on the timestamp field of a reading.
type ByTimestamp []MeterReading

func (a ByTimestamp) Len() int      { return len(a) }
func (a ByTimestamp) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a ByTimestamp) Less(i, j int) bool {
	return a[i].Reading.Timestamp.Unix() < a[j].Reading.Timestamp.Unix()
}

type DBClient struct {
	client       client.Client
	serverconfig *InfluxDBConfig
}

func NewDBClient(serverConfig *InfluxDBConfig) (*DBClient, error) {
	retval := new(DBClient)
	var err error
	retval.client, err = client.NewHTTPClient(client.HTTPConfig{
		Addr: fmt.Sprintf("http://%s:%d", serverConfig.Host,
			serverConfig.Port),
		Username: serverConfig.User,
		Password: serverConfig.Pass,
	})
	if err != nil {
		return nil, fmt.Errorf("Cannot create InfluxDB client: %s", err.Error())
	}

	// Save config for later use
	retval.serverconfig = serverConfig
	return retval, nil
}

func (dbc DBClient) MkDBPusher(dbchannel chan MeterReading) (func(), error) {
	log.Println("Getting list of databases:")
	response, err := dbc.client.Query(
		client.Query{
			Command:  "SHOW DATABASES",
			Database: dbc.serverconfig.Database,
		})
	if err != nil {
		log.Fatal(err)
	}
	if err == nil && response.Error() != nil {
		return nil, fmt.Errorf("Cannot retrieve list of InfluxDB databases: %s", response.Error())
	}
	log.Printf("Show databases response: %v", response.Results)
	foundDatabase := false
	// holy shit this is ugly
	for _, result := range response.Results {
		for _, row := range result.Series {
			if row.Name == "databases" {
				for _, values := range row.Values {
					for _, database := range values {
						log.Printf("found database: %s", database)
						if database == dbc.serverconfig.Database {
							foundDatabase = true
						}
					}
				}
			}
		}
	}

	if !foundDatabase {
		log.Fatalf("Did not find database \"%s\" - please create it",
			dbc.serverconfig.Database)
	}
	return func() {
		for {
			_, ok := <-dbchannel
			meterreading, ok := <-dbchannel
			if !ok {
				log.Fatal("Cannot read from internal channel - aborting")
			}
			//log.Printf("Pushing reading %v", meterreading.Reading)

			// TODO: At the moment, each value is written individually.
			// A batched transfer (e.g. all five seconds) would rock.
			// Create a new point batch
			bp, _ := client.NewBatchPoints(client.BatchPointsConfig{
				Database:  dbc.serverconfig.Database,
				Precision: "ms",
			})

			// Create a point and add to batch
			tags := map[string]string{"meterid": meterreading.MeterID}
			fields := map[string]interface{}{
				"value":     meterreading.Reading.Value,
				"timestamp": meterreading.Reading.Timestamp.Unix(),
			}
			pt, err := client.NewPoint(dbc.serverconfig.Database, tags, fields, time.Now())
			if err != nil {
				fmt.Println("Error: ", err.Error())
			}
			bp.AddPoint(pt)

			// Write the batch
			dbc.client.Write(bp)

		}
	}, nil
}

func (dbc DBClient) points2meterreadings(name string,
	res []client.Result) (retval []MeterReading) {
	for _, row := range res[0].Series {
		for _, v := range row.Values {
			json_timestamp := v[1].(json.Number)
			int_timestamp, _ := json_timestamp.Int64()
			timestamp := time.Unix(int_timestamp, 0)
			json_freq := v[2].(json.Number)
			freq, _ := json_freq.Float64()
			log.Printf("T: %d, F: %.3f", int_timestamp, freq)
			retval = append(retval, MeterReading{name, Reading{timestamp, freq}})
		}
	}

	return retval
}

func (dbc DBClient) GetFrequenciesBetween(meterID string,
	start time.Time, end time.Time) (retval []MeterReading, err error) {
	querystr := fmt.Sprintf("select timestamp, value from %s where meterid = '%s' and timestamp > %d and timestamp < %d", dbc.serverconfig.Database, meterID, start.Unix(), end.Unix())
	//log.Printf("Running query >%s<", querystr)
	q := client.Query{
		Command:  querystr,
		Database: dbc.serverconfig.Database,
	}
	if response, err := dbc.client.Query(q); err == nil {
		if response.Error() != nil {
			log.Printf("Failed to run query: %s", response.Error())
			return nil, response.Error()
		}
		retval = dbc.points2meterreadings(meterID, response.Results)
	} else {
		log.Printf("Failed to run query: %s", err.Error())
	}
	return retval, nil
}

func (dbc DBClient) GetLastFrequencies(meterID string, amount int) ([]MeterReading, error) {
	retval := []MeterReading{}
	querystr := fmt.Sprintf("select timestamp, value from %s where meterid= '%s' order by time desc limit %d", dbc.serverconfig.Database, meterID, amount)
	log.Printf("Running query >%s<", querystr)
	q := client.Query{
		Command:  querystr,
		Database: dbc.serverconfig.Database,
	}
	if response, err := dbc.client.Query(q); err == nil {
		if response.Error() != nil {
			log.Printf("Failed to run query: %s", response.Error())
			return nil, response.Error()
		}
		retval = dbc.points2meterreadings(meterID, response.Results)
	} else {
		log.Printf("Failed to run query: %s", err.Error())
	}
	return retval, nil
}

func (dbc DBClient) GetLastFrequency(meterID string) (MeterReading,
	error) {
	readings, error := dbc.GetLastFrequencies(meterID, 1)
	if error != nil {
		return MeterReading{}, error
	} else {
		return readings[0], error
	}
}
