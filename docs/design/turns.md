# Turns API

`internal/turns` implements on-demand accrual on the existing `accounts` row.
Each game constructs a service with its own immutable `Policy`: a positive
`int64` cap and an interval to earn one whole Turn. Intervals must be positive
whole microseconds, matching PostgreSQL timestamp precision. There are no
engine defaults or deployment environment variables for game mechanics.

`New(policy, clock)` validates configuration; a nil clock uses `time.Now`.
`Current(ctx, tx, accountID)` materializes regeneration and returns spendable
whole Turns. `Spend(ctx, tx, accountID, cost)` does the same and deducts a
nonnegative `int64` whole-Turn cost, returning the remaining spendable Turns.
Both require a caller-owned `*sql.Tx`, acquire `SELECT ... FOR UPDATE` on the
account **before sampling the clock**, and retain that lock until the caller
commits or rolls back. Call `Current` before preparing game actions to serialize
same-player preparation as well as spending. Use the same transaction for all
game effects. These are internal primitives, not the public game-plugin API.

Use PostgreSQL's default READ COMMITTED isolation. Higher isolation levels may
return serialization failures, which the caller must handle by retrying the
entire transaction. SQL errors are wrapped, including `sql.ErrNoRows` for an
unknown account. Invalid costs return `ErrInvalidCost`; insufficient funds
return `ErrInsufficient` without updating the row. Invalid stored numeric state
returns `ErrInvalidState`. Roll back the action transaction on any failure;
the service never commits or rolls back on the caller's behalf.

Elapsed fractional Turns are retained in the timestamp rather than rounded away
on each spend. Existing fractional NUMERIC balances are preserved, but only
their whole portion is spendable. At the cap, the balance is clamped and the
timestamp advances to the sampled time: excess accrual is not banked. Backward
clocks never earn Turns or move the timestamp backward. Arithmetic is exact
and capped before converting to `int64`, including very large stored balances
or elapsed periods. Injected clocks must be safe for concurrent calls.

A policy change applies the new interval to all elapsed time since the stored
timestamp, and the new cap at the next successful read or spend. There is no
policy history or automatic migration. To preserve old-policy accrual, a host
must quiesce actions and materialize balances under the old policy before
switching. Lowering a cap discards balances above it on the next successful
operation. Invalid or insufficient spends never materialize policy changes.
