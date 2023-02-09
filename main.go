package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/burntsushi/toml"
	"github.com/lib/pq"
)

var (
	configPath = flag.String("config", "check_graphite.conf", "path to the config file")
	daemon     = flag.Bool("daemon", false, "run as a daemon, requires a config file")
	addr       = flag.String("addr", "", "Set the address of the graphite server to use.")
	interval   = flag.String("interval", "60s", "Set the interval to use for checking")
	levelWarn  = flag.Float64("warn", 0, "Set the level when it should be a warning.")
	levelErr   = flag.Float64("error", 0, "Set the level when it should be an error")
	key        = flag.String("key", "", "The key to check for the levels")
	insecure   = flag.Bool("insecure", false, "Ignore SSL errors when sending requests")
	message    = flag.String("message", "current value: %f", "Create a result message based on the template. Use %f to place the numeric value. To write the % sign, write %%")
)

type (
	Config struct {
		DB        string `toml:"db"`
		CheckerID int    `toml:"checker_id"`
		Wait      int    `toml:"wait_duration"`
		Jobs      int    `toml:"jobs"`
	}

	States []int
)

type (
	Result []struct{ Datapoints [][]*float64 }
)

func main() {
	flag.Parse()
	var (
		config Config
		db     *sql.DB
	)

	hostname, err := os.Hostname()
	if err != nil {
		log.Fatalf("could not resolve hostname: %s", err)
	}

	if *daemon {
		if _, err := toml.DecodeFile(*configPath, &config); err != nil {
			Unknown("could not parse config file: %s", err)
		}
		db, err = sql.Open("postgres", config.DB)
		if err != nil {
			Unknown("could not open database connection: %s", err)
		}
	}

	rootCAs, _ := x509.SystemCertPool()
	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}

	tlsConfig := &tls.Config{
		RootCAs:            rootCAs,
		InsecureSkipVerify: *insecure,
	}
	tr := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: tr}

	if !*daemon {
		msg, exitCode := runCheck(client, *addr, *key, *message, *interval, *levelWarn, *levelErr)
		fmt.Println(msg)
		os.Exit(exitCode)
	}

	if config.Jobs == 0 {
		config.Jobs = 4
	}
	wg := &sync.WaitGroup{}
	for i := 0; i < config.Jobs; i++ {
		wg.Add(1)
		go func(thread int) {
			log.Printf("starting thread with id '%d'", thread)
			fs := flag.NewFlagSet(fmt.Sprintf("check_graphite thread %d", thread), flag.ContinueOnError)
			addr := fs.String("addr", "", "Set the address of the graphite server to use.")
			interval := fs.String("interval", "60s", "Set the interval to use for checking")
			levelWarn := fs.Float64("warn", 0, "Set the level when it should be a warning.")
			levelErr := fs.Float64("error", 0, "Set the level when it should be an error")
			key := fs.String("key", "", "The key to check for the levels")
			message := fs.String("message", "current value: %f", "Create a result message based on the template. Use %f to place the numeric value. To write the % sign, write %%")

			for {
				tx, err := db.Begin()
				if err != nil {
					log.Printf("could not start transaction: %s", err)
					continue
				}
				var (
					id      int64
					cmdLine []string
					states  States
					mapId   int
					state   int
					msg     string
				)
				row := tx.QueryRow(`select check_id, cmdLine, states, mapping_id
					from active_checks
					where next_time < now()
					and enabled
					and checker_id = $1
					order by next_time
					for update skip locked
					limit 1;`, config.CheckerID)
				err = row.Scan(&id, pq.Array(&cmdLine), &states, &mapId)
				if err != nil && err == sql.ErrNoRows {
					tx.Rollback()
					time.Sleep(time.Second * time.Duration(config.Wait))
					continue
				} else if err != nil {
					log.Printf("could not scan values: %s", err)
					tx.Rollback()
					time.Sleep(time.Second * time.Duration(config.Wait))
					continue
				}

				if err := fs.Parse(cmdLine[1:]); err != nil {
					msg = fmt.Sprintf("could not parse arguments: %s", err)
					state = 3
				} else {
					msg, state = runCheck(client, *addr, *key, *message, *interval, *levelWarn, *levelErr)
				}

				if _, err := tx.Exec(`update active_checks ac
                set next_time = now() + intval, states = ARRAY[$2::int] || states[1:4],
                                msg = $3,
                                acknowledged = case when $4 then false else acknowledged end,
                                state_since = case $2 when states[1] then state_since else now() end
                        where check_id = $1`, id, &state, &msg, states.ToOK()); err != nil {
					log.Printf("[%d] could not update row '%d': %s", thread, id, err)
					tx.Rollback()
					continue
				}
				if _, err := tx.Exec(`insert into notifications(check_id, states, output, mapping_id, notifier_id, check_host)
                        select $1, ac.states, $2, $3, cn.notifier_id, $4
                        from active_checks ac
                        join checks_notify cn on ac.check_id = cn.check_id
                        where ac.check_id = $1
                                and ac.acknowledged = false;`,
					&id, &msg, &mapId, &hostname); err != nil {
					log.Printf("[%d] could not create notification for '%d': %s", thread, id, err)
					tx.Rollback()
					continue
				}
				tx.Commit()
			}
		}(i)
	}
	wg.Wait()
}

// runCheck runs the check with the client and returns the resulting message and exit code.
func runCheck(c *http.Client, addr, key, msg, intVal string, levelWarn, levelErr float64) (string, int) {
	if addr == "" {
		return "no address given to check", 3
	}
	if intVal == "" {
		return "no interval given", 3
	}
	if key == "" {
		return "no key given", 3
	}

	url, err := url.Parse(addr)
	if err != nil {
		return fmt.Sprintf("could not parse addr '%s': %s", addr, err), 3
	}
	url.Path = url.Path + "/render"
	query := url.Query()
	query.Set("format", "json")
	query.Set("target", key)
	query.Set("from", "-"+intVal)
	url.RawQuery = query.Encode()

	var (
		res *http.Response
		raw []byte
	)
	success := false
	for i := 0; i < 2; i++ {
		res, err = c.Get(url.String())
		if err != nil {
			return fmt.Sprintf("could not get result: %s", err), 3
		}
		defer res.Body.Close()

		raw, err = ioutil.ReadAll(res.Body)
		if err != nil {
			return fmt.Sprintf("could not read content body: %s", err), 3
		}

		// For some reason metrictank is unable to return any data when it goes into
		// maintenance mode. There is no way to work around the issue, because of
		// its architecture.
		// So when it is not in the mood to return data, we just retry again.
		if res.StatusCode > 500 {
			continue
		}
		if res.StatusCode != http.StatusOK {
			return fmt.Sprintf("graphite api answered with status code %d", res.StatusCode), 3
		}
		success = true
		break
	}
	if !success {
		return fmt.Sprintf("graphite api has internal problems, answered with status code: %d", res.StatusCode), 3
	}

	payload := Result{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Sprintf("could not parse json content: %s\n%s", err, raw), 3
	}

	var curVal *float64
	exitCode := 0
	for _, target := range payload {
		for _, point := range target.Datapoints {
			if point[0] == nil {
				continue
			}
			if levelErr < levelWarn {
				if curVal == nil || *point[0] < *curVal {
					curVal = point[0]
				}
				if *point[0] <= levelErr && exitCode != 1 {
					exitCode = 2
				} else if *point[0] <= levelWarn && exitCode == 0 {
					exitCode = 1
				}
			} else {
				if curVal == nil || *point[0] > *curVal {
					curVal = point[0]
				}
				if *point[0] >= levelErr && exitCode != 1 {
					exitCode = 2
				} else if *point[0] >= levelWarn && exitCode == 0 {
					exitCode = 1
				}
			}
		}
	}
	if curVal == nil {
		log.Printf("number of targets received for key '%s': %d", key, len(payload))
		if len(payload) > 0 {
			for i, target := range payload {
				log.Printf("number of datapoints in target %d: %d", i, len(target.Datapoints))
			}
		}
		return "no values received! Is the host down?!", 2
	}
	return fmt.Sprintf(*message+"\n", *curVal), exitCode
}

func Unknown(msg string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, msg, args...)
	// TODO what is unknown exit code?
	os.Exit(3)
}

func (s *States) Value() (driver.Value, error) {
	last := len(*s)
	if last == 0 {
		return "{}", nil
	}
	result := strings.Builder{}
	_, err := result.WriteString("{")
	if err != nil {
		return "", fmt.Errorf("could not write to buffer: %s", err)
	}
	for i, state := range *s {
		if _, err := fmt.Fprintf(&result, "%d", state); err != nil {
			return "", fmt.Errorf("could not write to buffer: %s", err)
		}
		if i < last-1 {
			if _, err := result.WriteString(","); err != nil {
				return "", fmt.Errorf("could not write to buffer: %s", err)
			}
		}
	}
	if _, err := result.WriteString("}"); err != nil {
		return "", fmt.Errorf("could not write to buffer: %s", err)
	}
	return result.String(), nil
}

func (s *States) Scan(src interface{}) error {
	switch src := src.(type) {
	case []byte:
		tmp := bytes.Trim(src, "{}")
		states := bytes.Split(tmp, []byte(","))
		result := make([]int, len(states))
		for i, state := range states {
			var err error
			result[i], err = strconv.Atoi(string(state))
			if err != nil {
				return fmt.Errorf("could not parse element %s: %s", state, err)
			}
		}
		*s = result
		return nil
	default:
		return fmt.Errorf("could not convert %T to states", src)
	}
}

// Append prepends the new state before all others.
func (s *States) Add(state int) {
	vals := *s
	statePos := 5
	if len(vals) < 6 {
		statePos = len(vals)
	}
	*s = append([]int{state}, vals[:statePos]...)
	return
}

// ToOK returns true when the state returns from != 0 to 0.
func (s *States) ToOK() bool {
	vals := *s
	if len(vals) == 0 {
		return false
	}
	if len(vals) <= 1 {
		return vals[0] == 0
	}
	if vals[0] == 0 && vals[1] > 0 {
		return true
	}
	return false
}
