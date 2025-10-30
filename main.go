package main

import (
	"bytes"
	"context"
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

	"git.zero-knowledge.org/gibheer/monzero"
	"github.com/BurntSushi/toml"
	_ "github.com/lib/pq"
)

var (
	configPath = flag.String("config", "check_graphite.conf", "path to the config file")
	daemon     = flag.Bool("daemon", false, "run as a daemon, requires a config file")
	addr       = flag.String("addr", "", "Set the address of the graphite server to use.")
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
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r := runner{client: client}
		result := r.runCheck(
			monzero.Check{
				Command:   os.Args,
				ExitCodes: []int{},
			},
			ctx,
		)
		fmt.Println(result.Message)
		os.Exit(result.ExitCode)
	}

	if config.Jobs == 0 {
		config.Jobs = 4
	}
	wg := &sync.WaitGroup{}
	for i := 0; i < config.Jobs; i++ {
		wg.Add(1)
		go func(thread int) {
			r := &runner{
				client: client,
			}
			checker, err := monzero.NewChecker(monzero.CheckerConfig{
				DB:             db,
				Timeout:        30 * time.Second,
				HostIdentifier: hostname,
				Executor:       r.runCheck,
			})
			if err != nil {
				log.Fatalf("could not start checker: %s", err)
			}
			for {
				if err := checker.Next(); err != nil {
					if err != monzero.ErrNoCheck {
						log.Printf("error when getting the next check: %s", err)
					}
					time.Sleep(time.Duration(config.Wait) * time.Second)
				}
			}
		}(i)
	}
	wg.Wait()
}

type (
	runner struct {
		client *http.Client
	}
)

func (r *runner) runCheck(check monzero.Check, ctx context.Context) monzero.CheckResult {
	result := monzero.CheckResult{ExitCode: 3}

	fs := flag.NewFlagSet("check_graphite", flag.ContinueOnError)
	addr := fs.String("addr", "", "Set the address of the graphite server to use.")
	interval := fs.String("interval", "60s", "Set the interval to use for checking")
	levelWarn := fs.Float64("warn", 0, "Set the level when it should be a warning.")
	levelErr := fs.Float64("error", 0, "Set the level when it should be an error")
	key := fs.String("key", "", "The key to check for the levels")
	retries := fs.Int("retries", 0, "the number of retries before the check is returned as failed")
	message := fs.String("message", "current value: %f", "Create a result message based on the template. Use %f to place the numeric value. To write the % sign, write %%")

	if err := fs.Parse(check.Command[1:]); err != nil {
		result.Message = fmt.Sprintf("could not parse arguments: %s", err)
		return result
	}

	if *addr == "" {
		result.Message = "no address given to check"
		return result
	}
	if *interval == "" {
		result.Message = "no interval given"
		return result
	}
	if *key == "" {
		result.Message = "no key given"
		return result
	}

	url, err := url.Parse(*addr)
	if err != nil {
		result.Message = fmt.Sprintf("could not parse addr '%s': %s", *addr, err)
		return result
	}
	url.Path = url.Path + "/render"
	query := url.Query()
	query.Set("format", "json")
	query.Set("target", *key)
	query.Set("from", "-"+*interval)
	url.RawQuery = query.Encode()

	var (
		res *http.Response
		raw []byte
	)
	success := false

	for i := 0; i < *retries+1; i++ {
		res, err = r.client.Get(url.String())
		if err != nil {
			result.Message = fmt.Sprintf("could not get result: %s", err)
			return result
		}
		defer res.Body.Close()

		raw, err = ioutil.ReadAll(res.Body)
		if err != nil {
			result.Message = fmt.Sprintf("could not read content body: %s", err)
			return result
		}

		// For some reason metrictank is unable to return any data when it goes into
		// maintenance mode. There is no way to work around the issue, because of
		// its architecture.
		// So when it is not in the mood to return data, we just retry again.
		if res.StatusCode > 500 {
			continue
		}
		if res.StatusCode != http.StatusOK {
			result.Message = fmt.Sprintf("graphite api answered with status code %d", res.StatusCode)
			return result
		}
		success = true
		break
	}

	if !success {
		result.Message = fmt.Sprintf("graphite api has internal problems, answered with status code: %d", res.StatusCode)
		return result
	}

	payload := Result{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		result.Message = fmt.Sprintf("could not parse json content: %s\n%s", err, raw)
		return result
	}

	var curVal *float64
	result.ExitCode = 0
	for _, target := range payload {
		for _, point := range target.Datapoints {
			if point[0] == nil {
				continue
			}
			if *levelErr < *levelWarn {
				if curVal == nil || *point[0] < *curVal {
					curVal = point[0]
				}
				if *point[0] <= *levelErr && result.ExitCode != 1 {
					result.ExitCode = 2
				} else if *point[0] <= *levelWarn && result.ExitCode == 0 {
					result.ExitCode = 1
				}
			} else {
				if curVal == nil || *point[0] > *curVal {
					curVal = point[0]
				}
				if *point[0] >= *levelErr && result.ExitCode != 1 {
					result.ExitCode = 2
				} else if *point[0] >= *levelWarn && result.ExitCode == 0 {
					result.ExitCode = 1
				}
			}
		}
	}
	if curVal == nil {
		result.ExitCode = 2
		result.Message = "No values received for query! Is the host down?"
		return result
	}
	result.Message = fmt.Sprintf(*message+"\n", *curVal)
	return result
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
