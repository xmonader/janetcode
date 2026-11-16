package secreport

import (
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/weburl"
)

func shellProbe(look func(string) (string, error), abi func() int) *shell.Sandbox {
	return shell.ProbeSandbox(look, abi)
}

func cfgSearch(key string) weburl.SearchConfig {
	return weburl.SearchConfig{Provider: "brave", Key: key}
}
