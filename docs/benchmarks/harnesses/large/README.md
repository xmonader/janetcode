# Large-eval harness benchmarks (31–35)

Five tasks seeded with a ~160-file tree so a multi-turn coding run can
accumulate on the order of 1e6 **session** input+cache-read tokens (not one
1M-token request). Results here must not replace the 01–20 files in the
parent directory.

Harnesses: pi, janetcode, qwencode, hermes-agent, grok. No openclaw.

```sh
export DASHSCOPE_API_KEY=...
make harness-bench-large
```
