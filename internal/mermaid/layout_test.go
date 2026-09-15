package mermaid

import (
	"strings"
	"testing"
)

// Golden layouts. They are exact strings on purpose: a diagram is a
// picture, so the only way a layout regression shows up in a test is by
// comparing the picture. Each case is drawn at a wide terminal and at a
// narrow one, where the columns close up or the whole thing gives way to
// the source.
func TestLayoutGolden(t *testing.T) {
	cases := []struct {
		name     string
		width    int
		src      string
		want     string
		fallback bool
	}{
		{name: "flow", width: 80, src: "flowchart TD\n  A[Parse] --> B{Supported?}\n  B -->|yes| C[Lay out]\n  B -- no --> D[Show source]\n  C --> E[Transcript]\n  D --> E\n", want: `
          ┌───────┐
          │ Parse │
          └───────┘
              │
              │
              ▼
       ┌────────────┐
       │ Supported? │
       └────────────┘
              │
     ┌────────┴──────┐
     ▼ yes           ▼ no
┌─────────┐   ┌─────────────┐
│ Lay out │   │ Show source │
└─────────┘   └─────────────┘
     │               │
     └────────┬──────┘
              ▼
       ┌────────────┐
       │ Transcript │
       └────────────┘
`, fallback: false},
		{name: "flow", width: 48, src: "flowchart TD\n  A[Parse] --> B{Supported?}\n  B -->|yes| C[Lay out]\n  B -- no --> D[Show source]\n  C --> E[Transcript]\n  D --> E\n", want: `
          ┌───────┐
          │ Parse │
          └───────┘
              │
              │
              ▼
       ┌────────────┐
       │ Supported? │
       └────────────┘
              │
     ┌────────┴──────┐
     ▼ yes           ▼ no
┌─────────┐   ┌─────────────┐
│ Lay out │   │ Show source │
└─────────┘   └─────────────┘
     │               │
     └────────┬──────┘
              ▼
       ┌────────────┐
       │ Transcript │
       └────────────┘
`, fallback: false},
		{name: "lr", width: 80, src: "graph LR\n  A[client] --> B[router]\n  B -->|rpc| C[service]\n  B --> D[cache]\n", want: `
                               rpc    ┌─────────┐
                              ┌──────▶│ service │
┌────────┐         ┌────────┐ │       └─────────┘
│ client │────────▶│ router │─┤
└────────┘         └────────┘ │       ┌─────────┐
                              └──────▶│  cache  │
                                      └─────────┘
`, fallback: false},
		{name: "lr", width: 48, src: "graph LR\n  A[client] --> B[router]\n  B -->|rpc| C[service]\n  B --> D[cache]\n", want: `
                            ┌─────────┐
                         ┌─▶│ service │
┌────────┐    ┌────────┐ │  └─────────┘
│ client │───▶│ router │─┤
└────────┘    └────────┘ │  ┌─────────┐
                         └─▶│  cache  │
                            └─────────┘
`, fallback: false},
		{name: "seq", width: 80, src: "sequenceDiagram\n  participant U as User\n  participant J as janetcode\n  U->>J: /plan tests\n  J-->>U: plan.md\n", want: `
┌──────┐     ┌───────────┐
│ User │     │ janetcode │
└──────┘     └───────────┘
    │ /plan tests  │
    │──────────────▶
    │   plan.md    │
    ◀╌╌╌╌╌╌╌╌╌╌╌╌╌╌│
    │              │
`, fallback: false},
		{name: "seq", width: 48, src: "sequenceDiagram\n  participant U as User\n  participant J as janetcode\n  U->>J: /plan tests\n  J-->>U: plan.md\n", want: `
┌──────┐     ┌───────────┐
│ User │     │ janetcode │
└──────┘     └───────────┘
    │ /plan tests  │
    │──────────────▶
    │   plan.md    │
    ◀╌╌╌╌╌╌╌╌╌╌╌╌╌╌│
    │              │
`, fallback: false},
		{name: "st", width: 80, src: "stateDiagram-v2\n  [*] --> Idle\n  Idle --> Busy : work\n  Busy --> Idle : done\n  Busy --> [*] : fail\n", want: `
┌───────┐
│ start │
└───────┘
    │
    │
    ▼
┌──────┐
│ Idle │
└──────┘
    ▲ done
    │
    ▼ work
┌──────┐
│ Busy │
└──────┘
    │
    │
    ▼ fail
 ┌─────┐
 │ end │
 └─────┘
`, fallback: false},
		{name: "st", width: 48, src: "stateDiagram-v2\n  [*] --> Idle\n  Idle --> Busy : work\n  Busy --> Idle : done\n  Busy --> [*] : fail\n", want: `
┌───────┐
│ start │
└───────┘
    │
    │
    ▼
┌──────┐
│ Idle │
└──────┘
    ▲ done
    │
    ▼ work
┌──────┐
│ Busy │
└──────┘
    │
    │
    ▼ fail
 ┌─────┐
 │ end │
 └─────┘
`, fallback: false},
		{name: "skip", width: 80, src: "flowchart TD\n  A[start] --> B[middle]\n  A --> C[far]\n  B --> C\n", want: `
  ┌───────┐
  │ start │
  └───────┘
      │
     ┌┴──────┐
     ▼       │
┌────────┐   │
│ middle │   │
└────────┘   │
     │       │
     └┬──────┘
      ▼
   ┌─────┐
   │ far │
   └─────┘
`, fallback: false},
		{name: "skip", width: 48, src: "flowchart TD\n  A[start] --> B[middle]\n  A --> C[far]\n  B --> C\n", want: `
  ┌───────┐
  │ start │
  └───────┘
      │
     ┌┴──────┐
     ▼       │
┌────────┐   │
│ middle │   │
└────────┘   │
     │       │
     └┬──────┘
      ▼
   ┌─────┐
   │ far │
   └─────┘
`, fallback: false},
	}
	for _, tc := range cases {
		t.Run(tc.name+"@"+itoa(tc.width), func(t *testing.T) {
			lines, ok := Render(tc.src, tc.width)
			if ok == tc.fallback {
				t.Fatalf("ok = %v, want fallback = %v", ok, tc.fallback)
			}
			if tc.fallback {
				return
			}
			got := strings.Join(lines, "\n")
			if want := strings.Trim(tc.want, "\n"); got != want {
				t.Errorf("layout changed.\ngot:\n%s\n\nwant:\n%s", got, want)
			}
			for i, l := range lines {
				if n := runeLen(l); n > tc.width {
					t.Errorf("line %d is %d columns wide, over the %d given", i, n, tc.width)
				}
			}
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
