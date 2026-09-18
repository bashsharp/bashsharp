# RFC 0000: <title>

- Status: draft
- Author: <you>
- Date: <yyyy-mm-dd>

## The shape

One or two lines of Bash# showing the construct as a program would write it.

## Why

What a program cannot say today, in one paragraph. If bash can already say
it, stop here.

## Collision class

Run the shape through stock GNU Bash 5.3 (`bash -n` in a minimal completing
context). **R** — stock bash rejects it, so admitting it is purely additive.
**E** — stock bash accepts it (say what it means there) and the construct
needs a commit signal and a shell escape. Quote the bash output.

## Lowering

What ordinary Go this becomes. A construct that cannot lower to plain Go —
or that re-spells something Go already has — is out, however nice it reads.

## Inert with the flag off

Confirm the shape is unreachable under `--no-bashsharp` and `--posix`.

## Fixture

The `bashsharp-tests` case (file, expected output, expected status) that
proves it, and the one that proves the escape hatch when the class is E.
