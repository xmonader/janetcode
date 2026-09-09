# Does locating an edit by line range pay?

`edit_file` can be given `from_line`/`to_line` instead of `old_string`. The
motivation was measured: on the session benchmark janetcode is 1.75x slower
than Claude Code on sonnet, per-turn latency tracks output tokens, 79% of
output is tool arguments rather than prose, `edit_file` is the largest
single consumer, and 54% of its bytes are `old_string` — text retyped
purely to say *where* an edit goes.

## Mechanically it does exactly what it should

Adoption, and what it costs to express an edit:

| model / set | edits | by line range | `old_string` bytes |
|---|---:|---:|---:|
| sonnet, 3 fanout tasks x 5 runs | 120 | 100% | 7,059 → 0 |
| qwen3.7-plus, 9 fanout tasks x 3 runs | 145 | 65% | 3,160 → 1,245 (−61%) |

On sonnet `edit_file`'s share of output fell 56%, from 16,859 to 7,380
characters.

## End to end it did not move the number

qwen3.7-plus, 9 tasks x 3 runs per arm, paired bootstrap over tasks:

| | string edits | line edits | ratio |
|---|---:|---:|---|
| passed | 27/27 | 27/27 | — |
| context per solved | 20k | 20k | x1.05 [0.91, 1.21] · inside the noise |
| output tokens | 22k | 24k | x1.07 [0.93, 1.22] · inside the noise |

Both ratios point the wrong way and neither clears the interval. On the
earlier sonnet fanout set the direction was right and also did not clear:
output x0.88 [0.70, 1.03].

## Why the mechanism works and the total does not

Scale. On qwen's fanout runs `old_string` was 3,160 characters against
roughly 88,000 characters of output — under 4% — so cutting 61% of it saves
about 2%, which is far inside a 21% run-to-run spread. On sonnet the same
share was around 22% of output, which is where it would show.

So the honest position: the change is proven mechanically and unproven
end to end. It is kept because it cannot cost anything — the model chooses
the cheaper locator when it fits and the string form is untouched — and
because the profile it targets is real on sonnet. The measurement that
would settle it is the sonnet session benchmark, which needs an account
with credit on a provider serving sonnet.

## Reproduce

```
janetcode --eval evals/fanout --eval-runs 3 --eval-off lineedit --eval-json a.json
janetcode --eval evals/fanout --eval-runs 3 --eval-json b.json --eval-compare a.json
janetcode --eval-report b.json --eval-compare a.json    # offline, no API calls
```
