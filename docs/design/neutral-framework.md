# Neutral framework boundary

Status: early design. These decisions describe the intended boundary, not a
finished API or implemented game framework.

## Shared foundation

One StillWorld deployment hosts one persistent world. StillWorld provides
accounts and sessions, and is intended to provide a common action pipeline and
per-player Turns. Actions resolve immediately: validation, spending Turns,
and applying an action's effects must succeed or fail together in one database
transaction.

All games use that action model. A game defines its own actions, their costs,
entities, and effects; it does not replace or bypass the shared Turns and
transaction model. Each game configures its own Turns regeneration rate and
maximum balance. The specific values and configuration format are not yet
decided.

## Game-owned rules and data

Initially, game rules and entities are written in Go and compiled into the
game. This does not preclude data-driven content later. Games own their
database tables and migrations alongside StillWorld's tables in the same
database.

StillWorld should provide a way to invoke game-defined actions within the
shared transaction. The exact integration mechanism is not yet designed. The
current code uses Go `internal` packages, so supporting a separate downstream
repository will require an intentional public integration boundary rather than
assuming those packages can already be imported externally.

StillWorld does not prescribe a setting, theme, or specific game entities.
Era of Darkness is planned as a separate private downstream game with its
own rules, content, and presentation.

## Minimal example

Include a small neutral example game upstream: one action spends Turns to
change a persistent counter stored in a game-owned table. It should exercise
game registration, game migrations, and the shared action transaction once
those integration mechanisms exist. This is a minimal proof of the boundary,
not a full game or a demonstration of player-to-player interaction.

## Open design work

- Define the public Go integration API and how a downstream executable
  combines StillWorld and game code without modifying upstream files.
- Define how game migrations are discovered, versioned, and applied without
  conflicting with StillWorld's migrations.
- Define the action contract, including authorization, validation, error
  handling, transaction ownership, and concurrency.
- Specify Turns configuration, rounding and regeneration behavior, and
  behavior when settings change.
- Decide the text interface and how game actions are presented and submitted.

The current code does not yet implement the action pipeline, Turns
regeneration, game integration API, or example game.
