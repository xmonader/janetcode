# Harness invocation notes

Model `qwen3.7-plus` at `https://coding-intl.dashscope.aliyuncs.com/v1`. Key supplied via process environment only.

Initial 5×5 (all five tasks, 20m timeout, up to five concurrent):

- pi 4/5
- janetcode 4/5
- qwencode 5/5
- hermes-agent 1/5
- grok 5/5

Rerun of `32-large-rename` and `34-large-headers` after seed fixes (Widget in the rename tree; `hdr_test.go` colocated with `H000` in `pkg/`). New rows merged by task name into the existing JSON.

- pi 1/2
- janetcode 2/2
- qwencode 2/2
- hermes-agent 0/2
- grok 2/2
