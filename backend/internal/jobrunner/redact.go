package jobrunner

import "strings"

// RedactedValue replaces secret values in API responses and logs.
const RedactedValue = "[REDACTED]"

// secretArgFlags are command-line flags whose value is a credential.
var secretArgFlags = map[string]bool{
	"--password": true,
	"--token":    true,
}

// RedactArgs returns a copy of args that is safe to display or log: values of
// credential flags (--password x, --password=x) are replaced, and every other
// arg goes through the same patterns used for job output (URL userinfo,
// password=, token=, ...).
func RedactArgs(args []string) []string {
	if args == nil {
		return nil
	}
	out := make([]string, len(args))
	maskNext := false
	for i, a := range args {
		switch {
		case maskNext:
			out[i] = RedactedValue
			maskNext = false
		case secretArgFlags[a]:
			out[i] = a
			maskNext = true
		default:
			out[i] = redactFlagValue(a)
		}
	}
	return out
}

func redactFlagValue(a string) string {
	if k, _, ok := strings.Cut(a, "="); ok && secretArgFlags[k] {
		return k + "=" + RedactedValue
	}
	return redactSensitive(a)
}

// Public returns a copy of the job that is safe to send to clients: env
// override values are masked (keys are kept) and credential args are redacted.
func (j *Job) Public() *Job {
	if j == nil {
		return nil
	}
	c := *j
	c.Args = RedactArgs(j.Args)
	if j.EnvOverrides != nil {
		c.EnvOverrides = make(map[string]string, len(j.EnvOverrides))
		for k := range j.EnvOverrides {
			c.EnvOverrides[k] = RedactedValue
		}
	}
	return &c
}

// publicJobs maps Public over a slice.
func publicJobs(jobs []Job) []*Job {
	out := make([]*Job, len(jobs))
	for i := range jobs {
		out[i] = jobs[i].Public()
	}
	return out
}
