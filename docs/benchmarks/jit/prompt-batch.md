# Working in batches

Every response you send is a round trip that re-sends the whole conversation, so make each one count.

- Explore first: read what you need, in parallel calls where you can.
- Once you know what to change, make all the changes you already know about in the same response: several edits in one edit_file call (it takes a list), or several tool calls in one message, rather than one change per response.
- Run the build or tests in the same response as the edits they check, after them.
- Only stop to think again when a result tells you something you did not know.
