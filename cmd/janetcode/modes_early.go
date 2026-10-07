package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/projectinit"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/whatif"
)

// The modes that run before anything is loaded. Each reports whether it
// handled the run, so run() can return without reaching the composition
// root: none of these needs a configuration, a provider or a key, and
// several exist precisely because the configuration is the problem.

// earlyMode handles the flags that print something and exit.
func earlyMode(fs *flag.FlagSet, o *options) (bool, error) {
	switch {
	case o.showVersion:
		fmt.Println("janetcode", version)
	case o.selfUpdate:
		return true, selfUpdate()
	case o.uninstall:
		return true, uninstallMode(os.Stdout, o.purge)
	case o.purge:
		return true, headless.BadInput("--purge goes with --uninstall")
	case o.initCfg:
		if p, err := config.Path(); err == nil {
			if _, err := os.Stat(p); err == nil {
				return true, headless.BadInput("%s already exists and was not touched; janetcode --doctor checks it, /config in a session edits it", p)
			}
		}
		p, err := config.WriteExample()
		if err != nil {
			return true, err
		}
		fmt.Println("wrote", p)
	case o.initProject:
		cwd, err := os.Getwd()
		if err != nil {
			return true, err
		}
		res, err := projectinit.Run(cwd, false)
		if err != nil {
			return true, err
		}
		if res.Error != "" {
			// Said once, on stderr, with a next step a shell has: the
			// result's tip is /init force, which only a session takes.
			return true, headless.BadInput("%s; open it and add what is missing, or move it aside and run --init-project again", res.Error)
		}
		printResult(os.Stdout, res)
	case o.listSess:
		return true, printSessions()
	case o.sessionProfile != "":
		return true, sessionProfileMode(os.Stdout, o.sessionProfile, o.sessionProfileStale)
	case o.decisionsReplay != "":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return true, decisionsReplayMode(ctx, o.decisionsReplay)
	case o.decisionsAnnotate != "":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return true, decisionsAnnotateMode(ctx, o.decisionsAnnotate, o.decisionsAnnotateOut)
	case o.answerID != "":
		dir, err := session.Dir()
		if err != nil {
			return true, err
		}
		return true, answerMode(os.Stdout, dir, o.answerID, strings.TrimSpace(strings.Join(fs.Args(), " ")))
	case o.backupTo != "":
		return true, backupMode(os.Stdout, o.backupTo)
	case o.restoreFrom != "":
		return true, restoreMode(os.Stdout, o.restoreFrom)
	case o.runDoctor:
		// The checkup makes a live model call, so ctrl+c ends it rather
		// than waiting out the provider's own timeout.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ov := config.Overrides{Provider: o.provider, Model: o.model, Effort: o.effort, Theme: o.themeFlag}
		if o.outFormat == "json" {
			return true, doctorJSON(ctx, os.Stdout, ov, o.doctorFix)
		}
		return true, doctor(ctx, os.Stdout, ov, o.doctorFix)
	case o.mcpServe:
		return true, mcpServeMode(o)
	case o.pluginCmd != "":
		return true, pluginMode(os.Stdout, o.pluginCmd, fs.Args())
	case o.runProof:
		cwd, err := os.Getwd()
		if err != nil {
			return true, err
		}
		return true, runProofFlag(os.Stdout, cwd, strings.TrimSpace(strings.Join(fs.Args(), " ")))
	default:
		return false, nil
	}
	return true, nil
}

// checkModes rejects flag combinations that do not go together, and flags
// that need another one to mean anything. The first row that holds is the
// error, and a row's condition names the flags it is about, so adding a
// mode means adding its rows here.
func checkModes(o *options, initial string, format headless.Format) error {
	recipeRun, whatIfRun := o.recipeName != "", o.whatIf != ""
	for _, bad := range []struct {
		holds bool
		msg   string
	}{
		{initial != "" && o.promptText != "", "give the prompt either with -p or as an argument, not both"},
		{o.until != "" && o.promptText == "", "--until needs a headless prompt (-p)"},
		{o.decisionsReplay != "" && o.promptText != "", "--decisions-replay is a calibration run, not a session; drop -p"},
		{o.decisionsAnnotate != "" && o.promptText != "", "--decisions-annotate is an offline labelling run, not a session; drop -p"},
		{o.decisionsAnnotate != "" && o.decisionsReplay != "", "--decisions-annotate and --decisions-replay are separate runs"},
		{o.sessionProfile != "" && o.promptText != "", "--session-profile reads recorded runs, it does not start one; drop -p"},
		{o.doctorFix && !o.runDoctor, "--fix is --doctor's: run janetcode --doctor --fix"},
		{format != headless.FormatText && !o.headless() && o.watchCmd == "" && !o.recoverTurn && !o.runDoctor && !o.listModels, "--output-format applies to a headless run (-p, --input-format stream-json, --recover), --doctor or --list-models"},
		{o.listModels && format == headless.FormatStreamJSON, "--list-models prints text or json, not stream-json"},
		{o.inFormat != "" && o.inFormat != string(headless.InputText) && o.inFormat != string(headless.InputStreamJSON), "unknown --input-format " + o.inFormat + ": want text or stream-json"},
		{o.inFormat == string(headless.InputStreamJSON) && format != headless.FormatStreamJSON, "--input-format stream-json needs --output-format stream-json"},
		{o.inFormat == string(headless.InputStreamJSON) && (o.jsonSchema != "" || o.until != "" || o.watchCmd != "" || o.bg), "--input-format stream-json does not combine with --json-schema, --until, --watch or --bg"},
		{o.inFormat == string(headless.InputStreamJSON) && (o.web || o.acpMode), "--input-format stream-json is headless; drop --web/--acp"},
		{o.safeMode && (o.yolo || o.autoMode || o.autoEdits), "--safe-mode does not combine with --yolo, --auto or --auto-edits: it is for a session that asks before acting"},
		{o.maxWallTime < 0, "--max-wall-time must be 0 (no limit) or more"},
		{o.maxWallTime > 0 && !o.headless(), "--max-wall-time needs a headless run (-p or --input-format stream-json)"},
		{o.maxToolCalls < 0, "--max-tool-calls must be 0 (no limit) or more"},
		{o.maxToolCalls > 0 && !o.headless(), "--max-tool-calls needs a headless run (-p or --input-format stream-json)"},
		{o.jsonSchema != "" && o.promptText == "", "--json-schema needs a headless prompt (-p)"},
		{o.jsonSchema != "" && o.watchCmd != "", "--json-schema and --watch do not combine: a watch has no single answer"},
		{o.watchCmd != "" && o.until != "", "--watch and --until cannot be combined"},
		{o.web && o.acpMode, "--web and --acp cannot be combined"},
		{o.web && o.promptText != "", "--web is a session, not a one-shot; drop -p"},
		{o.bg && o.web, "--bg and --web cannot be combined"},
		{o.bg && o.acpMode, "--bg and --acp cannot be combined"},
		{recipeRun && o.promptText != "", "--recipe and -p do not combine; a recipe hands over to the model on its own if a step fails"},
		{recipeRun && o.watchCmd != "", "--recipe and --watch do not combine"},
		{recipeRun && o.web, "--web is a session, not a one-shot; drop --recipe"},
		{recipeRun && o.acpMode, "--recipe and --acp do not combine"},
		{recipeRun && o.bg, "--recipe and --bg do not combine"},
		{whatIfRun && o.promptText != "", "--what-if and -p do not combine"},
		{whatIfRun && (o.evalPath != "" || o.evalReport != ""), "--what-if and --eval do not combine"},
		{whatIfRun && o.replayPath != "", "--what-if and --replay do not combine"},
		{whatIfRun && recipeRun, "--what-if and --recipe do not combine"},
		{whatIfRun && o.web, "--web is a session, not a one-shot; drop --what-if"},
		{whatIfRun && o.acpMode, "--what-if and --acp do not combine"},
		{whatIfRun && o.bg, "--what-if and --bg do not combine"},
		{o.fork && o.resume == "" && !o.cont, "--fork copies the session --resume or --continue opens; name one"},
		{whatIfRun && o.whatIfFrom <= 0, "--what-if needs --from-turn <n> (1-based)"},
	} {
		if bad.holds {
			return fmt.Errorf("%w: %s", headless.ErrBadInput, bad.msg)
		}
	}
	return nil
}

// offlineMode handles the modes that read what is already on disk.
// Re-reading saved results and replaying a recording buy nothing, so they
// must not need a provider or a key: they run before the configuration is
// loaded, where a missing key would stop them.
func offlineMode(o *options) (bool, error) {
	switch {
	case o.evalReport != "":
		return true, reportEval(o.evalReport, o.evalCompare)
	case o.showSess != "":
		return true, showSession(os.Stdout, o.showSess)
	case o.searchSess != "":
		return true, searchSessionsMode(os.Stdout, o.searchSess)
	case o.auditSess != "":
		return true, auditMode(os.Stdout, o.auditSess)
	case o.replayPath != "":
		path, _, _, err := whatif.ResolveSession(o.replayPath)
		if err != nil {
			return true, err
		}
		return true, runReplay(path)
	case o.evalTrain != "":
		cfgPath, _ := config.Path()
		return true, trainCritic(strings.Split(o.evalTrain, ","), filepath.Join(filepath.Dir(cfgPath), "critic.json"), os.Stdout)
	}
	return false, nil
}
