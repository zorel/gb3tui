# gb3tui

A Bubble Tea terminal client for a [gb3](https://github.com/zorel/gb3)
`gb2c` server. It talks to the same HTTP/SSE API as the `gc2` web frontend and
the Android client.

## Build and run

```bash
go build -o gb3tui .
./gb3tui -url http://localhost:16666 -info dave -blocked thoasm -gray
```

Flags:

- `-url`: gb2c base URL (default `http://localhost:16666`);
- `-info`: nickname or user-agent sent with posts;
- `-tribunes`: comma-separated tribune names; empty means all;
- `-blocked`: comma-separated login/user-agent identities to hide;
- `-gray`: show filtered posts as gray rows with their messages hidden.

## Keys

- `↑`/`↓`: select a post;
- `←`/`→`: follow clock references (`12:00:00` or `12:00:00@tribune`);
- `Tab`/`Shift-Tab`: switch tribune;
- `PgUp`/`PgDn` or `Ctrl-U`/`Ctrl-D`: scroll;
- `Enter`: post, or insert the selected clock when the input is empty;
- `q`: quit when the input is empty.

The client loads server-side blocked identities from `/gb2c/config`. Replies are
grouped with a stable conversation color and one gutter lane per overlapping
conversation: `┏` marks a root, `┣` an intermediate reply and `┗` the end of a
branch. Inline `<b>`, `<i>`, `<u>` and `<s>` formatting is rendered with the
matching terminal style.

## Test

```bash
go test ./...
```
