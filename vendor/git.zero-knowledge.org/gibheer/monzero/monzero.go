package monzero

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

var (
	ErrNoCheck = fmt.Errorf("no check found to run")
)

type (
	// Checker maintains the state of checks that need to be run.
	Checker struct {
		db       *sql.DB
		id       int // id is the resolved checker id for this instance.
		executor func(Check, context.Context) CheckResult
		timeout  time.Duration
		ident    string // the host identifier
		logger   *slog.Logger
	}

	CheckerConfig struct {
		// CheckerID is used to find the checks that need to be run by this
		// instance.
		CheckerID int

		// DB is the connection to the database to use.
		DB *sql.DB

		// Timeout is the duration a check has time to run.
		// Set this to a reasonable value for all checks to avoid long running
		// checks blocking the execution.
		Timeout time.Duration

		// Executor receives a check and must run the requested command in the
		// time of the context.
		// At the end it must return a CheckResult.
		Executor func(Check, context.Context) CheckResult

		// HostIdentifier is used in notifications to point to the source of the
		// notification.
		HostIdentifier string

		// Checker will send debug details to the logger for each command executed.
		Logger *slog.Logger
	}

	// Check is contains the metadata to run a check and its current state.
	Check struct {
		// Command is the command to run as stored in the database.
		Command []string
		// ExitCodes contains the list of exit codes of past runs.
		ExitCodes []int

		id        int64 // the check instance id
		mappingId int   // ID to map the result for this check
	}

	// CheckResult is the result of a check. It may contain a message
	// and must contain an exit code.
	// The exit code should conform to the nagios specification of
	// 0 - okay
	// 1 - error
	// 2 - warning
	// 3 - unknown or executor errors
	// Other codes are also okay and may be mapped to different values, but
	// need further configuration in the system.
	CheckResult struct {
		ExitCode int
		Message  string // Message will be shown in the frontend for context
	}
)

func NewChecker(cfg CheckerConfig) (*Checker, error) {
	c := &Checker{db: cfg.DB,
		executor: cfg.Executor,
		timeout:  cfg.Timeout,
		ident:    cfg.HostIdentifier,
		logger:   cfg.Logger,
	}
	if c.executor == nil {
		return nil, fmt.Errorf("executor must not be nil")
	}

	return c, nil
}

// Next pulls the next check in line and runs the set executor.
// The result is then updated in the database and a notification generated.
func (c *Checker) Next() error {
	check := Check{}
	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("could not start database transaction: %w", err)
	}
	defer tx.Rollback()
	err = tx.
		QueryRow(`select check_id, cmdLine, states, mapping_id
			from active_checks
			where next_time < now()
				and enabled
				and checker_id = $1
			order by next_time
			for update skip locked
			limit 1;`, c.id).
		Scan(&check.id, &check.Command, &check.ExitCodes, &check.mappingId)
	if err != nil {
		if err == sql.ErrNoRows {
			return ErrNoCheck
		}
		return fmt.Errorf("could not get next check: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	result := c.executor(check, ctx)
	if ctx.Err() == context.DeadlineExceeded {
		result.Message = fmt.Sprintf("check took longer than %s", c.timeout)
		result.ExitCode = 2
	}
	c.logger.Debug(
		"check command run",
		"id", check.id,
		"command", check.Command,
		"exit code", result.ExitCode,
		"message", result.Message,
	)

	backToOkay := false
	if len(check.ExitCodes) == 0 && result.ExitCode == 0 {
		backToOkay = true
	} else if len(check.ExitCodes) > 0 && check.ExitCodes[0] > 0 && result.ExitCode == 0 {
		backToOkay = true
	}

	if _, err := tx.Exec(`update active_checks ac
		set next_time = now() + intval, states = ARRAY[$2::int] || states[1:4],
				msg = $3,
				acknowledged = case when $4 then false else acknowledged end,
				state_since = case $2 when states[1] then state_since else now() end
			where check_id = $1`, check.id, result.ExitCode, result.Message, backToOkay); err != nil {
		return fmt.Errorf("could not update check '%d': %w", check.id, err)
	}

	if _, err := tx.Exec(`insert into notifications(check_id, states, output, mapping_id, notifier_id, check_host)
			select $1, array_agg(ml.target), $2, $3, cn.notifier_id, $4
			from active_checks ac
			cross join lateral unnest(ac.states) s
			join checks_notify cn on ac.check_id = cn.check_id
			join mapping_level ml on ac.mapping_id = ml.mapping_id and s.s = ml.source
			where ac.check_id = $1
				and ac.acknowledged = false
				and cn.enabled = true 
			group by cn.notifier_id;`, check.id, result.Message, check.mappingId, c.ident); err != nil {
		return fmt.Errorf("could not create notification '%d': %s", check.id, err)
	}
	tx.Commit()
	return nil
}
