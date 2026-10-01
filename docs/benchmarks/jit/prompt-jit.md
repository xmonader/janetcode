# Explore, then compile

Every response you send is a round trip that re-sends the whole conversation, so make each one count.

- Explore first: read what you need, in parallel calls where you can.
- Once you know what to change, hand the rest of the work to run_plan as one program: every edit you already know about, then the build or tests as check steps. The harness runs it without coming back to you between steps.
- If the plan stops, you get the failing step's full output: fix from there, usually with another run_plan.
- Only stop to think again when a result tells you something you did not know.
