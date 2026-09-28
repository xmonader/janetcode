package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	curatorFile   = "curator.json"
	usageFile     = "skill-usage.json"
	archivedDir   = "archived-skills"
	projectSkills = "skills"
)

type curatorState struct {
	Pinned []string `json:"pinned"`
}

type usageEntry struct {
	Used int    `json:"used"`
	Last string `json:"last,omitempty"`
}

// Auto reports whether this skill was generated rather than written by hand.
func (s Skill) Auto() bool {
	o := strings.ToLower(strings.TrimSpace(s.Origin))
	if o == "auto-skill" || o == "learned" || o == "auto" {
		return true
	}
	return strings.HasPrefix(s.Name, "auto-skill-") || strings.HasPrefix(s.Name, "learned-")
}

// Curate runs /curator: list, pin, archive or restore auto-skills.
func Curate(cwd string, stats map[string]Stat, args string) (string, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 || fields[0] == "list" {
		return listAuto(cwd, stats), nil
	}
	if len(fields) < 2 {
		return "", fmt.Errorf("usage: /curator [list | pin <name> | archive <name> | restore <name>]")
	}
	cmd, name := fields[0], fields[1]
	switch cmd {
	case "pin":
		return pinSkill(cwd, name)
	case "archive":
		return archiveSkill(cwd, name)
	case "restore":
		return restoreSkill(cwd, name)
	default:
		return "", fmt.Errorf("usage: /curator [list | pin <name> | archive <name> | restore <name>]")
	}
}

func listAuto(cwd string, stats map[string]Stat) string {
	skills := Load(cwd, "")
	st := loadState(cwd)
	usage := loadUsage(cwd)
	pinned := map[string]bool{}
	for _, n := range st.Pinned {
		pinned[n] = true
	}
	var live []Skill
	for _, s := range skills {
		if s.Auto() {
			live = append(live, s)
		}
	}
	var sb strings.Builder
	if len(live) == 0 {
		sb.WriteString("no auto-skills in this project\n")
	} else {
		sb.WriteString("auto-skills\n")
		sort.Slice(live, func(i, j int) bool { return live[i].Name < live[j].Name })
		for _, s := range live {
			used := usage[s.Name].Used
			if stats != nil {
				if stt, ok := stats[s.Name]; ok && stt.Used > used {
					used = stt.Used
				}
			}
			mark := ""
			if pinned[s.Name] {
				mark = "  pinned"
			}
			fmt.Fprintf(&sb, "  %-24s used %d%s\n", s.Name, used, mark)
		}
	}
	archived := listArchived(cwd)
	if len(archived) > 0 {
		sb.WriteString("archived\n")
		for _, n := range archived {
			fmt.Fprintf(&sb, "  %s\n", n)
		}
	}
	sb.WriteString("/curator pin|archive|restore <name>")
	saveUsageFrom(cwd, usage, stats)
	return strings.TrimRight(sb.String(), "\n")
}

func pinSkill(cwd, name string) (string, error) {
	s, err := findProjectAuto(cwd, name)
	if err != nil {
		return "", err
	}
	st := loadState(cwd)
	for _, n := range st.Pinned {
		if n == s.Name {
			return s.Name + " is already pinned", nil
		}
	}
	st.Pinned = append(st.Pinned, s.Name)
	sort.Strings(st.Pinned)
	if err := saveState(cwd, st); err != nil {
		return "", err
	}
	return "pinned " + s.Name, nil
}

func archiveSkill(cwd, name string) (string, error) {
	s, err := findProjectAuto(cwd, name)
	if err != nil {
		return "", err
	}
	st := loadState(cwd)
	for _, n := range st.Pinned {
		if n == s.Name {
			return "", fmt.Errorf("%s is pinned; /curator cannot archive it until you unpin (edit .janetcode/curator.json)", s.Name)
		}
	}
	dstDir := filepath.Join(cwd, ".janetcode", archivedDir)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	src := s.Path
	if s.Dir != "" {
		src = s.Dir
	}
	dst := filepath.Join(dstDir, filepath.Base(src))
	if err := moveNew(src, dst); err != nil {
		return "", err
	}
	return "archived " + s.Name + " → " + dst, nil
}

func restoreSkill(cwd, name string) (string, error) {
	src, err := findArchived(cwd, name)
	if err != nil {
		return "", err
	}
	dstDir := filepath.Join(cwd, ".janetcode", projectSkills)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dstDir, filepath.Base(src))
	if err := moveNew(src, dst); err != nil {
		return "", err
	}
	return "restored " + filepath.Base(src), nil
}

// moveNew renames src to dst unless something is already at dst: rename
// replaces a file there without a word, which lost an auto-skill
// regenerated since its namesake was archived.
func moveNew(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s already exists; move or remove it first", dst)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

func findProjectAuto(cwd, name string) (Skill, error) {
	for _, s := range Load(cwd, "") {
		if s.Auto() && strings.EqualFold(s.Name, name) {
			if s.Source != "project" {
				return Skill{}, fmt.Errorf("%s is a %s skill; /curator only archives project auto-skills", s.Name, s.Source)
			}
			return s, nil
		}
	}
	return Skill{}, fmt.Errorf("no auto-skill named %q", name)
}

func findArchived(cwd, name string) (string, error) {
	dir := filepath.Join(cwd, ".janetcode", archivedDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("nothing archived")
	}
	for _, e := range entries {
		n := strings.TrimSuffix(e.Name(), ".md")
		if strings.EqualFold(n, name) || strings.EqualFold(e.Name(), name) {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("nothing archived named %q", name)
}

func listArchived(cwd string) []string {
	entries, err := os.ReadDir(filepath.Join(cwd, ".janetcode", archivedDir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(out)
	return out
}

func janetcodeDir(cwd string) string {
	return filepath.Join(cwd, ".janetcode")
}

func loadState(cwd string) curatorState {
	data, err := os.ReadFile(filepath.Join(janetcodeDir(cwd), curatorFile))
	if err != nil {
		return curatorState{}
	}
	var st curatorState
	_ = json.Unmarshal(data, &st)
	return st
}

func saveState(cwd string, st curatorState) error {
	dir := janetcodeDir(cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, curatorFile), append(data, '\n'), 0o644)
}

func loadUsage(cwd string) map[string]usageEntry {
	data, err := os.ReadFile(filepath.Join(janetcodeDir(cwd), usageFile))
	if err != nil {
		return map[string]usageEntry{}
	}
	var m map[string]usageEntry
	if json.Unmarshal(data, &m) != nil {
		return map[string]usageEntry{}
	}
	return m
}

func saveUsageFrom(cwd string, usage map[string]usageEntry, stats map[string]Stat) {
	if stats == nil {
		return
	}
	if usage == nil {
		usage = map[string]usageEntry{}
	}
	changed := false
	now := time.Now().UTC().Format(time.RFC3339)
	for name, st := range stats {
		if st.Used == 0 {
			continue
		}
		cur := usage[name]
		if st.Used > cur.Used {
			cur.Used = st.Used
			cur.Last = now
			usage[name] = cur
			changed = true
		}
	}
	if !changed {
		return
	}
	dir := janetcodeDir(cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(usage, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, usageFile), append(data, '\n'), 0o644)
}
