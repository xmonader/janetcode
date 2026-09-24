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
	case o.runDoctor:
		// The checkup makes a live model call, so ctrl+c ends it rather
		// than waiting out the provider's own timeout.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return true, doctor(ctx, os.Stdout, config.Overrides{Provider: o.provider, Model: o.model, Effort: o.effort, Theme: o.themeFlag}, o.doctorFix)
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
		{format != headless.FormatText && o.promptText == "" && o.watchCmd == "", "--output-format applies to a headless run (-p)"},
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
