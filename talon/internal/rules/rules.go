package rules

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"

	yaml "gopkg.in/yaml.v3"

	"github.com/Issif/falco-talon/configuration"
	"github.com/Issif/falco-talon/internal/events"
	"github.com/Issif/falco-talon/utils"
)

type Action struct {
	Name         string                 `yaml:"action"`
	Actionner    string                 `yaml:"actionner"`
	Parameters   map[string]interface{} `yaml:"parameters,omitempty"`
	Continue     string                 `yaml:"continue,omitempty"`      // can't be a bool because an omitted value == false by default
	IgnoreErrors string                 `yaml:"ignore_errors,omitempty"` // can't be a bool because an omitted value == false by default
}

type Rule struct {
	Name      string    `yaml:"rule"`
	Continue  string    `yaml:"continue"`          // can't be a bool because an omitted value == false by default
	DryRun    string    `yaml:"dry_run,omitempty"` // can't be a bool because an omitted value == false by default
	Actions   []*Action `yaml:"actions"`
	Notifiers []string  `yaml:"notifiers"`
	Match     Match     `yaml:"match"`
}

type Match struct {
	OutputFields       []string `yaml:"output_fields"`
	OutputFieldsC      [][]outputfield
	PriorityComparator string
	Priority           string   `yaml:"priority,omitempty"`
	Source             string   `yaml:"source,omitempty"`
	Rules              []string `yaml:"rules"`
	Tags               []string `yaml:"tags"`
	TagsC              [][]string
	PriorityNumber     int
}

type outputfield struct {
	Key        string
	Comparator string
	Value      string
}

const (
	trueStr  string = "true"
	falseStr string = "false"
)

var rules *[]*Rule

var (
	priorityCheckRegex       *regexp.Regexp
	actionCheckRegex         *regexp.Regexp
	priorityComparatorRegex  *regexp.Regexp
	tagCheckRegex            *regexp.Regexp
	outputFieldKeyCheckRegex *regexp.Regexp
)

func init() {
	priorityCheckRegex = regexp.MustCompile(`(?i)^(<|>)?(=)?(Debug|Informational|Notice|Warning|Error|Critical|Alert|Emergency|)$`)
	actionCheckRegex = regexp.MustCompile(`[a-z]+:[a-z]+`)
	priorityComparatorRegex = regexp.MustCompile(`^(<|>)?(=)?`)
	tagCheckRegex = regexp.MustCompile(`(?i)^[a-z_0-9.]*[a-z0-9]$`)
	outputFieldKeyCheckRegex = regexp.MustCompile(`(?i)^[a-z0-9.\[\]]*(!)?(=)`)

	rules = new([]*Rule)
}

func ParseRules(files []string) *[]*Rule {
	config := configuration.GetConfiguration()

	a, r, err := extractActionsRules(files)
	if err != nil {
		utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: err.Error(), Message: "rules"})
		return nil
	}

	for _, rule := range *r {
		for n := range rule.Actions {
			for _, action := range *a {
				if rule.Actions[n].Name == action.Name {
					if rule.Actions[n].Actionner == "" && action.Actionner != "" {
						rule.Actions[n].Actionner = action.Actionner
					}
					if rule.Actions[n].IgnoreErrors == "" && action.IgnoreErrors != "" {
						rule.Actions[n].IgnoreErrors = action.IgnoreErrors
					}
					if rule.Actions[n].Continue == "" && action.Continue != "" {
						rule.Actions[n].Continue = action.Continue
					}
					if rule.Actions[n].Parameters == nil && len(action.Parameters) != 0 {
						rule.Actions[n].Parameters = make(map[string]interface{})
					}
					for k, v := range action.Parameters {
						rt := reflect.TypeOf(v)
						ru := reflect.TypeOf(rule.Actions[n].Parameters[k])
						if v == nil {
							continue
						}
						if rule.Actions[n].Parameters[k] != nil && ru.Kind() != rt.Kind() {
							utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: "mismatch of type for a parameter", Message: "rules", Rule: rule.GetName(), Action: action.GetName()})
							continue
						}
						switch rt.Kind() {
						case reflect.Slice, reflect.Array:
							w := v
							if rule.Actions[n].Parameters[k] == nil {
								rule.Actions[n].Parameters[k] = []interface{}{w}
							} else {
								w = append(w.([]interface{}), rule.Actions[n].Parameters[k].([]interface{})...)
							}
							rule.Actions[n].Parameters[k] = w
						case reflect.Map:
							for s, t := range v.(map[string]interface{}) {
								if rule.Actions[n].Parameters[k] == nil {
									rule.Actions[n].Parameters[k] = make(map[string]interface{})
									rule.Actions[n].Parameters[k].(map[string]interface{})[s] = t
								}
							}
						default:
							if rule.Actions[n].Parameters[k] == nil {
								rule.Actions[n].Parameters[k] = v
							}
						}
					}
				}
			}
		}
		for _, j := range rule.Match.Tags {
			t := strings.Split(strings.ReplaceAll(j, " ", ""), ",")
			rule.Match.TagsC = append(rule.Match.TagsC, t)
		}
		for _, j := range rule.Match.OutputFields {
			t := strings.Split(strings.ReplaceAll(strings.ReplaceAll(j, "!=", "!"), ", ", ","), ",")
			o := []outputfield{}
			for _, k := range t {
				if strings.Contains(k, "=") {
					p := strings.Split(k, "=")
					if len(p) == 2 {
						o = append(o, outputfield{p[0], "=", strings.ReplaceAll(p[1], `"`, "")})
					}
				}
				if strings.Contains(k, "!") {
					p := strings.Split(k, "!")
					if len(p) == 2 {
						o = append(o, outputfield{p[0], "!=", strings.ReplaceAll(p[1], `"`, "")})
					}
				}
			}
			rule.Match.OutputFieldsC = append(rule.Match.OutputFieldsC, o)
		}
	}

	valid := true // to check the validity of the rules
	for _, i := range *r {
		if !i.isValid() {
			valid = false
		}
	}

	if !valid {
		return nil
	}

	rules = r

	return rules
}

func extractActionsRules(files []string) (*[]*Action, *[]*Rule, error) {
	if len(files) == 0 {
		return nil, nil, errors.New("no rule file is provided")
	}

	a := make([]*Action, 0)
	r := make([]*Rule, 0)

	for _, i := range files {
		at := make([]*Action, 0)
		rt := make([]*Rule, 0)
		f, err := os.ReadFile(i)
		if err != nil {
			return nil, nil, err
		}

		if err := yaml.Unmarshal(f, &at); err != nil {
			return nil, nil, fmt.Errorf("wrong syntax for the rule file '%v': %v", files[0], err.Error())
		}
		if err := yaml.Unmarshal(f, &rt); err != nil {
			return nil, nil, fmt.Errorf("wrong syntax for the rule file '%v': %v", files[0], err.Error())
		}

		a = append(a, at...)
		r = append(r, rt...)
	}

	for n, i := range a {
		if n == len(a)-1 {
			break
		}
		if i.Name == "" {
			continue
		}
		for _, l := range a[n+1:] {
			if l.Name != "" && i.Name != "" && i.Name == l.Name {
				if l.Actionner != "" {
					i.Actionner = l.Actionner
				}
				if l.Continue != "" {
					i.Continue = l.Continue
				}
				if l.IgnoreErrors != "" {
					i.IgnoreErrors = l.IgnoreErrors
				}
				if i.Parameters == nil && len(l.Parameters) != 0 {
					i.Parameters = make(map[string]interface{})
				}
				for k, v := range l.Parameters {
					rt := reflect.TypeOf(v)
					ru := reflect.TypeOf(i.Parameters[k])
					if v == nil {
						continue
					}
					if i.Parameters[k] != nil && ru.Kind() != rt.Kind() {
						continue
					}
					switch rt.Kind() {
					case reflect.Slice, reflect.Array:
						if i.Parameters[k] == nil {
							i.Parameters[k] = []interface{}{v}
						} else {
							i.Parameters[k] = append(i.Parameters[k].([]interface{}), v.([]interface{})...)
						}
					case reflect.Map:
						for s, t := range v.(map[string]interface{}) {
							if i.Parameters[k] == nil {
								i.Parameters[k] = make(map[string]interface{})
							}
							i.Parameters[k].(map[string]interface{})[s] = t
						}
					default:
						i.Parameters[k] = v
					}
				}
				l.Name = ""
			}
		}
	}

	for n, i := range r {
		if n == len(a)-1 {
			break
		}
		if i.Name == "" {
			continue
		}
		for _, l := range r[n+1:] {
			if l.Name != "" && i.Name == l.Name {
				if l.Continue != "" {
					i.Continue = l.Continue
				}
				if l.DryRun != "" {
					i.DryRun = l.DryRun
				}
				i.Notifiers = append(i.Notifiers, l.Notifiers...)
				i.Match.OutputFields = append(i.Match.OutputFields, l.Match.OutputFields...)
				i.Match.Priority = l.Match.Priority
				i.Match.Source = l.Match.Source
				i.Match.Rules = append(i.Match.Rules, l.Match.Rules...)
				i.Match.Tags = append(i.Match.Tags, l.Match.Tags...)
				i.Actions = append(i.Actions, l.Actions...)

				l.Name = ""
			}
		}
	}

	af := make([]*Action, 0)
	rf := make([]*Rule, 0)

	for _, i := range a {
		if i.Name != "" {
			af = append(af, i)
		}
	}
	for _, i := range r {
		if i.Name != "" {
			rf = append(rf, i)
		}
	}

	return &af, &rf, nil
}

func (rule *Rule) isValid() bool {
	config := configuration.GetConfiguration()
	valid := true
	if rule.Name == "" {
		utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: "all rules must have a name", Message: "rules"})
		valid = false
	}
	if rule.Continue != "" && rule.Continue != trueStr && rule.Continue != falseStr {
		utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: "'continue' setting can be 'true' or 'false' only", Message: "rules", Rule: rule.Name})
		valid = false
	}
	if rule.DryRun != "" && rule.DryRun != trueStr && rule.DryRun != falseStr {
		utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: "'dry_run' setting can be 'true' or 'false' only", Message: "rules", Rule: rule.Name})
		valid = false
	}
	if len(rule.Actions) == 0 {
		utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: "no action specified", Message: "rules", Rule: rule.Name})
		valid = false
	}
	if len(rule.Actions) != 0 {
		for _, i := range rule.Actions {
			if i.Name == "" {
				utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: "action without a name", Message: "rules", Rule: rule.Name})
				valid = false
			}
			if i.Actionner == "" {
				utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: "missing actionner", Message: "rules", Action: i.Name, Rule: rule.Name})
				valid = false
			}
			if !actionCheckRegex.MatchString(i.Actionner) {
				utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: "incorrect actionner", Message: "rules", Action: i.Name, Actionner: i.Actionner, Rule: rule.Name})
				valid = false
			}
			if i.Continue != "" && i.Continue != trueStr && i.Continue != falseStr {
				utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: "'continue' setting can be 'true' or 'false' only", Message: "rules", Action: i.Name, Actionner: i.Actionner, Rule: rule.Name})
				valid = false
			}
			if i.IgnoreErrors != "" && i.IgnoreErrors != trueStr && i.IgnoreErrors != falseStr {
				utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: "'ignore_errors' setting can be 'true' or 'false' only", Message: "rules", Action: i.Name, Actionner: i.Actionner, Rule: rule.Name})
				valid = false
			}
		}
	}
	if !priorityCheckRegex.MatchString(rule.Match.Priority) {
		utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: fmt.Sprintf("incorrect priority '%v'", rule.Match.Priority), Message: "rules", Rule: rule.Name})
		valid = false
	}
	for _, i := range rule.Match.TagsC {
		for _, j := range i {
			if !tagCheckRegex.MatchString(j) {
				utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: fmt.Sprintf("incorrect tag '%v'", j), Message: "rules", Rule: rule.Name})
				valid = false
			}
		}
	}
	for _, i := range rule.Match.OutputFields {
		t := strings.Split(strings.ReplaceAll(i, ", ", ","), ",")
		for _, j := range t {
			if !outputFieldKeyCheckRegex.MatchString(j) {
				utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: fmt.Sprintf("incorrect output field key '%v'", j), Message: "rules", Rule: rule.Name})
				valid = false
			}
		}
	}
	if err := rule.setPriorityNumberComparator(); err != nil {
		utils.PrintLog("error", config.LogFormat, utils.LogLine{Error: fmt.Sprintf("incorrect priority comparator '%v'", rule.Match.PriorityComparator), Message: "rules", Rule: rule.Name})
		valid = false
	}
	return valid
}

func (rule *Rule) setPriorityNumberComparator() error {
	if rule.Match.Priority == "" {
		return nil
	}
	rule.Match.PriorityComparator = priorityComparatorRegex.FindAllString(rule.Match.Priority, -1)[0]
	rule.Match.PriorityNumber = getPriorityNumber(priorityComparatorRegex.ReplaceAllString(rule.Match.Priority, ""))
	return nil
}

func GetRules() *[]*Rule {
	return rules
}

func (rule *Rule) GetName() string {
	return rule.Name
}

func (rule *Rule) GetActions() []*Action {
	return rule.Actions
}

func (rule *Rule) GetNotifiers() []string {
	return rule.Notifiers
}

func (action *Action) GetName() string {
	return action.Name
}

func (action *Action) GetActionner() string {
	return action.Actionner
}

func (action *Action) GetActionnerCategory() string {
	return strings.Split(action.Actionner, ":")[0]
}

func (action *Action) GetActionnerName() string {
	return strings.Split(action.Actionner, ":")[1]
}

func (action *Action) GetParameters() map[string]interface{} {
	return action.Parameters
}

func (rule *Rule) CompareRule(event *events.Event) bool {
	if !rule.compareRules(event) {
		return false
	}
	if !rule.compareOutputFields(event) {
		return false
	}
	if !rule.comparePriority(event) {
		return false
	}
	if !rule.compareTags(event) {
		return false
	}
	if !rule.compareSource(event) {
		return false
	}
	return true
}

func (rule *Rule) compareRules(event *events.Event) bool {
	if len(rule.Match.Rules) == 0 {
		return true
	}
	for _, i := range rule.Match.Rules {
		if event.Rule == i {
			return true
		}
	}
	return false
}

func (rule *Rule) compareOutputFields(event *events.Event) bool {
	if len(rule.Match.OutputFields) == 0 {
		return true
	}
	for _, i := range rule.Match.OutputFieldsC {
		var countK, countV, countF int
		for _, j := range i {
			if j.Comparator == "=" {
				countK++
			}
		}
		for _, j := range i {
			for k, v := range event.OutputFields {
				if j.Comparator == "!=" && j.Key == k && j.Value == fmt.Sprintf("%v", v) {
					countF++
				}
				if j.Comparator == "=" && j.Key == k && j.Value == fmt.Sprintf("%v", v) {
					countV++
				}
			}
		}
		if countK == countV && countF == 0 {
			return true
		}
	}
	return false
}

func (rule *Rule) compareTags(event *events.Event) bool {
	if len(rule.Match.TagsC) == 0 {
		return true
	}
	for _, i := range rule.Match.TagsC {
		count := 0
		for _, j := range i {
			for _, k := range event.Tags {
				if k == j {
					count++
				}
			}
		}
		if count == len(i) {
			return true
		}
	}
	return false
}

func (rule *Rule) compareSource(event *events.Event) bool {
	if rule.Match.Source == "" {
		return true
	}
	return event.Source == rule.Match.Source
}

func (rule *Rule) comparePriority(event *events.Event) bool {
	if rule.Match.PriorityNumber == 0 {
		return true
	}
	switch rule.Match.PriorityComparator {
	case ">":
		if getPriorityNumber(event.Priority) > rule.Match.PriorityNumber {
			return true
		}
	case ">=":
		if getPriorityNumber(event.Priority) >= rule.Match.PriorityNumber {
			return true
		}
	case "<":
		if getPriorityNumber(event.Priority) < rule.Match.PriorityNumber {
			return true
		}
	case "<=":
		if getPriorityNumber(event.Priority) <= rule.Match.PriorityNumber {
			return true
		}
	default:
		if getPriorityNumber(event.Priority) == rule.Match.PriorityNumber {
			return true
		}
	}
	return false
}
