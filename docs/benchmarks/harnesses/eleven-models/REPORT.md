# Five harnesses on eleven models: tokens in, cached, out, and cost

- Harnesses: janetcode, pi, hermes-agent, openclaw, qwencode
- Models: `qwen3.7-plus` and `qwen3.6-plus` on DashScope; `mercury-2.5`,
  `deepseek-v4-pro-0813`, `qwen3.8-flash`, `qwen3.7-flash`, `glm-5.3-flash`,
  `laguna-s-2.1`, `ling-3.0-flash`, `gemma-4-31b-it` and `mimo-v2.5` on
  OpenRouter
- Tasks: `evals/` 01–20, three complete runs per harness per model —
  3,300 task runs, all recorded 2026-09-10 with one launcher
- Per-model detail, failures and reproduction commands are in each
  `<model>-five/REPORT.md` beside this one

## How to read the columns

**tokens in (fresh)** is prompt tokens the endpoint charged at full price.
**cached** is prompt tokens it reported as served from cache. **out** is
completion tokens. **total read** is the three added together — the
context the model actually processed, whatever it cost.

Every harness reports its own usage and the launcher records only what it
emitted; nothing is estimated. Where a harness reports no cache figure on
most tasks (marked `*`) its fresh column is its whole prompt and its cost
an upper bound. That is hermes-agent and openclaw on qwen3.6-plus, and
hermes-agent on glm-5.3-flash and qwen3.7-flash for a third of tasks.

**cost** is computed, not reported, from one price source for all eleven
models — OpenRouter's list prices on 2026-09-10 — as
`fresh × p_in + cached × p_cache + out × p_out`, with each model's three
rates in its table heading. The DashScope endpoint bills in its own
currency and tiers; the two qwen-plus figures are indicative against the
rest. Where a harness reported cost itself (pi and janetcode on
OpenRouter) it agrees with the computed figure to the fourth decimal.
**per solved task** is cost divided by tasks passed, in cents. **spread**
is the difference between the largest and smallest of the three runs'
fresh input.

## The short version, 660 tasks per harness

| harness | passed | cost, all eleven | per solved task | total read per 60 tasks |
|---|---:|---:|---:|---|
| janetcode | **651/660** | **$1.17** | **0.18¢** | 0.97–1.39M |
| hermes-agent | 633/660 | $1.61 | 0.25¢ | 0.74–1.42M |
| pi | 639/660 | $2.99 | 0.47¢ | 3.7–5.9M |
| qwencode | 645/660 | $7.65 | 1.19¢ | 8.2–12.0M |
| openclaw | 632/660 | $8.09 | 1.28¢ | 7.1–9.1M |

- **janetcode has the best pass rate and the lowest cost per solved
  task**, and is first or second on cost on every one of the eleven
  models. It is cheapest outright on four (both qwen-plus, mercury,
  qwen3.7-flash) and second on the other seven.
- **hermes-agent is the cheapest harness on six models**, all of them the
  small OpenRouter ones, by sending the least fresh input. It pays for it
  in tasks: 633 passed against janetcode's 651, with three-minute
  timeouts and files written where the check did not look. Its cost per
  solved task is 40% above janetcode's.
- **pi is cheapest once**, on deepseek, where the endpoint credited 97%
  of its prompt as cached. Its rank elsewhere is decided by cache credit:
  third on nine models, fourth on qwen3.6-plus where it was credited
  nothing.
- **qwencode and openclaw are never cheaper than fourth.** They read
  7–12M tokens per 60 tasks on every model; the other three read 1–6M.
  Their pass rates are ordinary (645 and 632), so the extra context buys
  nothing the tasks can measure.
- **Output is not where the money goes.** Every harness writes 250–350k
  tokens over the 660 tasks; janetcode the least. The 7x cost range is
  entirely the prompt side.
- **Pass rates are close**: 632–651 of 660. On this suite the harness
  decides the bill, not whether the work gets done.

## Per model

### qwen3.7-plus  (fresh $0.32/M · cache read $0.064/M · output $1.28/M)

| harness | passed | tokens in (fresh) | cached | out | total read | cost | per solved task | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 60/60 | 288,648 | 711,913 | 23,837 | 1,024,398 | $0.1684 | 0.28¢ | 1% |
| pi | 59/60 | 353,528 | 3,891,712 | 24,449 | 4,269,689 | $0.3935 | 0.67¢ | 114% |
| hermes-agent | 59/60 | 489,009 | 769,024 | 40,879 | 1,298,912 | $0.2580 | 0.44¢ | 18% |
| openclaw | 59/60 | 1,167,962 | 6,274,688 | 28,445 | 7,471,095 | $0.8117 | 1.38¢ | 6% |
| qwencode | 60/60 | 2,599,690 | 6,009,734 | 25,634 | 8,635,058 | $1.2493 | 2.08¢ | 0% |

### qwen3.6-plus  (fresh $0.325/M · cache read $0.065/M · output $1.95/M)

| harness | passed | tokens in (fresh) | cached | out | total read | cost | per solved task | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 58/60 | 286,219 | 662,376 | 18,579 | 967,174 | $0.1723 | 0.30¢ | 1% |
| pi | 58/60 | 4,093,574 | 0 | 23,055 | 4,116,629 | $1.3754 | 2.37¢ | 13% |
| hermes-agent | 57/60 | 1,384,325 | 0 * | 33,777 | 1,418,102 | $0.5158 | 0.90¢ | 8% |
| openclaw | 57/60 | 7,796,214 | 0 * | 26,827 | 7,823,041 | $2.5861 | 4.54¢ | 10% |
| qwencode | 58/60 | 2,626,378 | 5,634,794 | 24,075 | 8,285,247 | $1.2668 | 2.18¢ | 3% |

### mercury-2.5  (fresh $0.04/M · cache read $0.004/M · output $0.15/M)

| harness | passed | tokens in (fresh) | cached | out | total read | cost | per solved task | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 57/60 | 466,940 | 566,228 | 30,294 | 1,063,462 | $0.0255 | 0.04¢ | 47% |
| pi | 58/60 | 1,125,961 | 3,180,907 | 32,447 | 4,339,315 | $0.0626 | 0.11¢ | 294% |
| hermes-agent | 59/60 | 806,263 | 209,552 | 40,962 | 1,056,777 | $0.0392 | 0.07¢ | 12% |
| openclaw | 56/60 | 2,836,216 | 4,572,463 | 53,311 | 7,461,990 | $0.1397 | 0.25¢ | 129% |
| qwencode | 58/60 | 5,014,210 | 3,193,476 | 29,560 | 8,237,246 | $0.2178 | 0.38¢ | 35% |

### deepseek-v4-pro-0813  (fresh $1.0494/M · cache read $0.03498/M · output $3.1482/M)

| harness | passed | tokens in (fresh) | cached | out | total read | cost | per solved task | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 60/60 | 311,325 | 674,048 | 17,460 | 1,002,833 | $0.4053 | 0.68¢ | 3% |
| pi | 60/60 | 146,516 | 4,164,096 | 20,703 | 4,331,315 | $0.3646 | 0.61¢ | 26% |
| hermes-agent | 60/60 | 321,281 | 681,984 | 20,958 | 1,024,223 | $0.4270 | 0.71¢ | 3% |
| openclaw | 59/60 | 2,292,517 | 5,397,504 | 22,712 | 7,712,733 | $2.6661 | 4.52¢ | 21% |
| qwencode | 60/60 | 2,309,218 | 6,123,264 | 27,352 | 8,459,834 | $2.7236 | 4.54¢ | 1% |

### qwen3.8-flash  (fresh $0.15/M · cache read $0.016/M · output $0.47/M)

| harness | passed | tokens in (fresh) | cached | out | total read | cost | per solved task | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 59/60 | 379,891 | 918,436 | 36,929 | 1,335,256 | $0.0890 | 0.15¢ | 12% |
| pi | 56/60 | 233,878 | 5,661,824 | 50,075 | 5,945,777 | $0.1492 | 0.27¢ | 25% |
| hermes-agent | 60/60 | 204,660 | 862,976 | 29,833 | 1,097,469 | $0.0585 | 0.10¢ | 19% |
| openclaw | 56/60 | 851,308 | 6,592,192 | 25,820 | 7,469,320 | $0.2453 | 0.44¢ | 34% |
| qwencode | 57/60 | 2,036,414 | 9,897,472 | 62,084 | 11,995,970 | $0.4930 | 0.86¢ | 42% |

### qwen3.7-flash  (fresh $0.03/M · cache read $0.006/M · output $0.13/M)

| harness | passed | tokens in (fresh) | cached | out | total read | cost | per solved task | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 60/60 | 298,758 | 811,228 | 27,707 | 1,137,693 | $0.0174 | 0.03¢ | 4% |
| pi | 58/60 | 241,894 | 4,308,736 | 27,832 | 4,578,462 | $0.0367 | 0.06¢ | 389% |
| hermes-agent | 59/60 | 944,613 | 383,360 | 37,286 | 1,365,259 | $0.0355 | 0.06¢ | 11% |
| openclaw | 55/60 | 2,643,749 | 6,005,632 | 35,389 | 8,684,770 | $0.1199 | 0.22¢ | 56% |
| qwencode | 60/60 | 3,566,825 | 5,710,080 | 27,956 | 9,304,861 | $0.1449 | 0.24¢ | 25% |

### glm-5.3-flash  (fresh $0.075/M · cache read $0.015/M · output $0.25/M)

| harness | passed | tokens in (fresh) | cached | out | total read | cost | per solved task | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 60/60 | 559,503 | 394,304 | 17,910 | 971,717 | $0.0524 | 0.09¢ | 104% |
| pi | 57/60 | 1,085,616 | 2,596,096 | 12,674 | 3,694,386 | $0.1235 | 0.22¢ | 21% |
| hermes-agent | 52/60 | 505,545 | 226,944 * | 11,021 | 743,510 | $0.0441 | 0.08¢ | 68% |
| openclaw | 58/60 | 2,269,673 | 5,250,752 | 19,448 | 7,539,873 | $0.2538 | 0.44¢ | 11% |
| qwencode | 60/60 | 1,659,512 | 6,865,408 | 29,424 | 8,554,344 | $0.2348 | 0.39¢ | 54% |

### laguna-s-2.1  (fresh $0.09/M · cache read $0.009/M · output $0.18/M)

| harness | passed | tokens in (fresh) | cached | out | total read | cost | per solved task | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 58/60 | 351,065 | 708,224 | 22,003 | 1,081,292 | $0.0419 | 0.07¢ | 32% |
| pi | 56/60 | 225,247 | 4,287,264 | 32,767 | 4,545,278 | $0.0648 | 0.12¢ | 415% |
| hermes-agent | 54/60 | 314,135 | 551,808 | 27,841 | 893,784 | $0.0382 | 0.07¢ | 28% |
| openclaw | 57/60 | 2,116,739 | 6,679,392 | 31,538 | 8,827,669 | $0.2563 | 0.45¢ | 15% |
| qwencode | 58/60 | 2,216,506 | 6,793,280 | 35,846 | 9,045,632 | $0.2671 | 0.46¢ | 4% |

### ling-3.0-flash  (fresh $0.021/M · cache read $0.004/M · output $0.06/M)

| harness | passed | tokens in (fresh) | cached | out | total read | cost | per solved task | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 60/60 | 587,763 | 481,984 | 22,112 | 1,091,859 | $0.0156 | 0.03¢ | 8% |
| pi | 59/60 | 571,375 | 3,943,104 | 26,996 | 4,541,475 | $0.0294 | 0.05¢ | 11% |
| hermes-agent | 59/60 | 361,903 | 632,320 | 31,451 | 1,025,674 | $0.0120 | 0.02¢ | 11% |
| openclaw | 56/60 | 4,631,700 | 3,173,184 | 25,578 | 7,830,462 | $0.1115 | 0.20¢ | 7% |
| qwencode | 57/60 | 3,624,126 | 5,624,704 | 29,291 | 9,278,121 | $0.1004 | 0.18¢ | 8% |

### gemma-4-31b-it  (fresh $0.09/M · cache read $0.05/M · output $0.34/M)

| harness | passed | tokens in (fresh) | cached | out | total read | cost | per solved task | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 59/60 | 868,310 | 505,457 | 17,246 | 1,391,013 | $0.1093 | 0.19¢ | 11% |
| pi | 58/60 | 671,890 | 4,722,048 | 25,889 | 5,419,827 | $0.3054 | 0.53¢ | 59% |
| hermes-agent | 54/60 | 630,691 | 663,488 | 48,383 | 1,342,562 | $0.1064 | 0.20¢ | 49% |
| openclaw | 60/60 | 3,877,120 | 3,248,320 | 10,485 | 7,135,925 | $0.5149 | 0.86¢ | 41% |
| qwencode | 59/60 | 3,415,968 | 5,446,132 | 19,456 | 8,881,556 | $0.5864 | 0.99¢ | 60% |

### mimo-v2.5  (fresh $0.14/M · cache read $0.003/M · output $0.28/M)

| harness | passed | tokens in (fresh) | cached | out | total read | cost | per solved task | spread |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 60/60 | 451,359 | 573,216 | 18,945 | 1,043,520 | $0.0702 | 0.12¢ | 25% |
| pi | 60/60 | 490,355 | 3,534,336 | 20,045 | 4,044,736 | $0.0849 | 0.14¢ | 49% |
| hermes-agent | 60/60 | 439,712 | 645,952 | 23,743 | 1,109,407 | $0.0701 | 0.12¢ | 64% |
| openclaw | 59/60 | 2,533,542 | 6,558,208 | 20,611 | 9,112,361 | $0.3801 | 0.64¢ | 0% |
| qwencode | 58/60 | 2,477,630 | 5,768,768 | 21,549 | 8,267,947 | $0.3702 | 0.64¢ | 16% |

### cost at list price, $ per 60 tasks

| model | janetcode | pi | hermes-agent | openclaw | qwencode |
|---|---:|---:|---:|---:|---:|
| qwen3.7-plus | $0.168 | $0.393 | $0.258 | $0.812 | $1.249 |
| qwen3.6-plus | $0.172 | $1.375 | $0.516 | $2.586 | $1.267 |
| mercury-2.5 | $0.025 | $0.063 | $0.039 | $0.140 | $0.218 |
| deepseek-v4-pro-0813 | $0.405 | $0.365 | $0.427 | $2.666 | $2.724 |
| qwen3.8-flash | $0.089 | $0.149 | $0.059 | $0.245 | $0.493 |
| qwen3.7-flash | $0.017 | $0.037 | $0.035 | $0.120 | $0.145 |
| glm-5.3-flash | $0.052 | $0.124 | $0.044 | $0.254 | $0.235 |
| laguna-s-2.1 | $0.042 | $0.065 | $0.038 | $0.256 | $0.267 |
| ling-3.0-flash | $0.016 | $0.029 | $0.012 | $0.111 | $0.100 |
| gemma-4-31b-it | $0.109 | $0.305 | $0.106 | $0.515 | $0.586 |
| mimo-v2.5 | $0.070 | $0.085 | $0.070 | $0.380 | $0.370 |
| **all eleven** | $1.167 | $2.990 | $1.605 | $8.086 | $7.654 |

### passed

| model | janetcode | pi | hermes-agent | openclaw | qwencode |
|---|---:|---:|---:|---:|---:|
| qwen3.7-plus | 60/60 | 59/60 | 59/60 | 59/60 | 60/60 |
| qwen3.6-plus | 58/60 | 58/60 | 57/60 | 57/60 | 58/60 |
| mercury-2.5 | 57/60 | 58/60 | 59/60 | 56/60 | 58/60 |
| deepseek-v4-pro-0813 | 60/60 | 60/60 | 60/60 | 59/60 | 60/60 |
| qwen3.8-flash | 59/60 | 56/60 | 60/60 | 56/60 | 57/60 |
| qwen3.7-flash | 60/60 | 58/60 | 59/60 | 55/60 | 60/60 |
| glm-5.3-flash | 60/60 | 57/60 | 52/60 | 58/60 | 60/60 |
| laguna-s-2.1 | 58/60 | 56/60 | 54/60 | 57/60 | 58/60 |
| ling-3.0-flash | 60/60 | 59/60 | 59/60 | 56/60 | 57/60 |
| gemma-4-31b-it | 59/60 | 58/60 | 54/60 | 60/60 | 59/60 |
| mimo-v2.5 | 60/60 | 60/60 | 60/60 | 59/60 | 58/60 |
| **all eleven** | **651/660** | **639/660** | **633/660** | **632/660** | **645/660** |

### fresh input tokens

| model | janetcode | pi | hermes-agent | openclaw | qwencode |
|---|---:|---:|---:|---:|---:|
| qwen3.7-plus | 288,648 | 353,528 | 489,009 | 1,167,962 | 2,599,690 |
| qwen3.6-plus | 286,219 | 4,093,574 | 1,384,325 | 7,796,214 | 2,626,378 |
| mercury-2.5 | 466,940 | 1,125,961 | 806,263 | 2,836,216 | 5,014,210 |
| deepseek-v4-pro-0813 | 311,325 | 146,516 | 321,281 | 2,292,517 | 2,309,218 |
| qwen3.8-flash | 379,891 | 233,878 | 204,660 | 851,308 | 2,036,414 |
| qwen3.7-flash | 298,758 | 241,894 | 944,613 | 2,643,749 | 3,566,825 |
| glm-5.3-flash | 559,503 | 1,085,616 | 505,545 | 2,269,673 | 1,659,512 |
| laguna-s-2.1 | 351,065 | 225,247 | 314,135 | 2,116,739 | 2,216,506 |
| ling-3.0-flash | 587,763 | 571,375 | 361,903 | 4,631,700 | 3,624,126 |
| gemma-4-31b-it | 868,310 | 671,890 | 630,691 | 3,877,120 | 3,415,968 |
| mimo-v2.5 | 451,359 | 490,355 | 439,712 | 2,533,542 | 2,477,630 |
| **all eleven** | 4,849,781 | 9,239,834 | 6,402,137 | 33,016,740 | 31,546,477 |

### cache read tokens

| model | janetcode | pi | hermes-agent | openclaw | qwencode |
|---|---:|---:|---:|---:|---:|
| qwen3.7-plus | 711,913 | 3,891,712 | 769,024 | 6,274,688 | 6,009,734 |
| qwen3.6-plus | 662,376 | 0 | 0 * | 0 * | 5,634,794 |
| mercury-2.5 | 566,228 | 3,180,907 | 209,552 | 4,572,463 | 3,193,476 |
| deepseek-v4-pro-0813 | 674,048 | 4,164,096 | 681,984 | 5,397,504 | 6,123,264 |
| qwen3.8-flash | 918,436 | 5,661,824 | 862,976 | 6,592,192 | 9,897,472 |
| qwen3.7-flash | 811,228 | 4,308,736 | 383,360 | 6,005,632 | 5,710,080 |
| glm-5.3-flash | 394,304 | 2,596,096 | 226,944 * | 5,250,752 | 6,865,408 |
| laguna-s-2.1 | 708,224 | 4,287,264 | 551,808 | 6,679,392 | 6,793,280 |
| ling-3.0-flash | 481,984 | 3,943,104 | 632,320 | 3,173,184 | 5,624,704 |
| gemma-4-31b-it | 505,457 | 4,722,048 | 663,488 | 3,248,320 | 5,446,132 |
| mimo-v2.5 | 573,216 | 3,534,336 | 645,952 | 6,558,208 | 5,768,768 |
| **all eleven** | 7,007,414 | 40,290,123 | 5,627,408 | 53,752,335 | 67,067,112 |

### output tokens

| model | janetcode | pi | hermes-agent | openclaw | qwencode |
|---|---:|---:|---:|---:|---:|
| qwen3.7-plus | 23,837 | 24,449 | 40,879 | 28,445 | 25,634 |
| qwen3.6-plus | 18,579 | 23,055 | 33,777 | 26,827 | 24,075 |
| mercury-2.5 | 30,294 | 32,447 | 40,962 | 53,311 | 29,560 |
| deepseek-v4-pro-0813 | 17,460 | 20,703 | 20,958 | 22,712 | 27,352 |
| qwen3.8-flash | 36,929 | 50,075 | 29,833 | 25,820 | 62,084 |
| qwen3.7-flash | 27,707 | 27,832 | 37,286 | 35,389 | 27,956 |
| glm-5.3-flash | 17,910 | 12,674 | 11,021 | 19,448 | 29,424 |
| laguna-s-2.1 | 22,003 | 32,767 | 27,841 | 31,538 | 35,846 |
| ling-3.0-flash | 22,112 | 26,996 | 31,451 | 25,578 | 29,291 |
| gemma-4-31b-it | 17,246 | 25,889 | 48,383 | 10,485 | 19,456 |
| mimo-v2.5 | 18,945 | 20,045 | 23,743 | 20,611 | 21,549 |
| **all eleven** | 253,022 | 296,932 | 346,134 | 300,164 | 332,227 |

### rank on cost within each model (1 = cheapest)

| model | janetcode | pi | hermes-agent | openclaw | qwencode |
|---|---:|---:|---:|---:|---:|
| qwen3.7-plus | 1 | 3 | 2 | 4 | 5 |
| qwen3.6-plus | 1 | 4 | 2 | 5 | 3 |
| mercury-2.5 | 1 | 3 | 2 | 4 | 5 |
| deepseek-v4-pro-0813 | 2 | 1 | 3 | 4 | 5 |
| qwen3.8-flash | 2 | 3 | 1 | 4 | 5 |
| qwen3.7-flash | 1 | 3 | 2 | 4 | 5 |
| glm-5.3-flash | 2 | 3 | 1 | 5 | 4 |
| laguna-s-2.1 | 2 | 3 | 1 | 4 | 5 |
| ling-3.0-flash | 2 | 3 | 1 | 5 | 4 |
| gemma-4-31b-it | 2 | 3 | 1 | 4 | 5 |
| mimo-v2.5 | 2 | 3 | 1 | 5 | 4 |
| **cheapest on** | 4 of 11 | 1 of 11 | 6 of 11 | 0 of 11 | 0 of 11 |


## Where the tokens go

Total read per 60 tasks is nearly a constant per harness across eleven
models: janetcode 0.97–1.39M, hermes-agent 0.74–1.42M, pi 3.7–5.9M,
openclaw 7.1–9.1M, qwencode 8.2–12.0M. That constancy is the signature of
a fixed per-call prefix — system prompt, tool schemas, injected files —
re-sent on every model call. openclaw's own result object reports a
31k-character system prompt and 67k characters of tool schemas on every
call; qwencode's prefix is of the same order; janetcode's is about 1.8k
tokens and hermes-agent's is small too, which is why those two stay near
1M while doing the same work in about the same number of calls.

What changes between models is how much of that prefix the endpoint
charges at full price. openclaw on qwen3.8-flash was credited 89% of its
prompt as cached and is 2.2x janetcode on fresh input; on qwen3.6-plus it
was credited nothing and is 27x. The same harness, the same prefix, a
12x difference in the bill.

## Cache credit is the noise floor

Several run-to-run spreads that look like model noise are the endpoint's
cache accounting. On glm-5.3-flash janetcode's fresh input varies 104%
between runs while its prompt total (fresh plus cached) varies 2%; on
qwen3.7-flash pi's fresh varies 389% while its prompt total varies 15%;
on gemma pi's prompt totals are identical to 0.1% while its fresh column
swings 59%. Anyone using fresh input alone to measure a harness change on
these endpoints will see effects that are not there. Compare prompt
totals, or run on an endpoint whose credit is steady — ling-3.0-flash and
mimo-v2.5 were, and qwen3.7-plus was.

The mercury spreads are the model: pi 294%, janetcode 47%, with prompt
totals moving the same way.

## Failures worth knowing about

- `06-read-and-answer`, the one task judged from the final message, is
  the most failed task in the matrix. Small models often answer without
  opening the file (laguna 8 of 15 attempts; qwen3.6-plus through
  openclaw 3 of 3) or miscount the loop (`3`, `7`, `10`, `26`, `31`).
- `17-csv-sort` is the second: a byte-for-byte check that CRLF or a
  trailing newline fails. qwencode on ling failed it 3 of 3; janetcode
  fails it now and then when the model writes through its Python sandbox.
- `13-multi-file-package`: pi and openclaw repeatedly write a nested
  `stack/go.mod`, so their own test passes inside the directory and the
  check from the module root finds no package.
- hermes-agent's misses are mostly files written where the check did not
  look and three-minute timeouts; its tool calls are not counted by the
  launcher, so which is not on record.

## Caveats

- Prices are list prices from one aggregator on one day; DashScope bills
  the two qwen-plus models differently.
- Unreported cache is charged at full price, which penalises hermes-agent
  and openclaw on qwen3.6-plus in particular.
- Nine of the eleven series ran two or three at a time against OpenRouter,
  so wall times there are under shared load; the qwen3.8-flash series ran
  alone because the model was rate-limited under that load.
- `deepseek-v4.1-flash`, the newest DeepSeek listing that day, could not be
  run: its only OpenRouter endpoint trains on prompts and the account's
  privacy setting excludes such endpoints, at request level too; DeepSeek's
  own API reported an insufficient balance.
- The tasks are twenty single-prompt jobs at the models' ceiling. They
  measure what a harness costs to do easy work, not how well it does hard
  work; pass rates within 3% of each other say so.
