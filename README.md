# StillWorld

An open-source foundation for persistent, text-first multiplayer strategy games.

## Purpose

StillWorld is an early-stage project exploring a foundation for persistent
multiplayer worlds. Its intended experience is primarily text-based, with
minimal visuals. Players are expected to take asynchronous actions that spend
from a shared action-point pool called **Turns**. Turns represent action costs,
not rounds of turn-based combat.

Era of Darkness is a future game that may use StillWorld; that possibility is
not a commitment or a description of a finished game.

## Status

StillWorld is in its initial stages. Its specific mechanics, features, and
implementation have not yet been designed. This README describes the project's
direction, not a feature-complete system.

The HTTP authentication endpoints are experimental and are not yet ready for
public deployment. State-changing requests must include a matching `Origin`,
same-origin Fetch Metadata, or `X-Stillworld-Request: 1` for non-browser clients.
Cross-site Fetch Metadata is rejected even when the custom header is present.
The expected public scheme is HTTPS by default, including behind a TLS
terminating proxy; `STILLWORLD_INSECURE_COOKIES=true` switches it to HTTP for
local development only. When using `STILLWORLD_BEHIND_PROXY=true`, restrict
direct access to the application and ensure the trusted proxy sets the
rightmost `X-Forwarded-For` value.

## Clean-room development

StillWorld is being developed independently. Other games may inform broad
genre-level ideas only; the project does not use or derive code, schemas,
templates, prose, or implementation details from the old Era of Darkness
repository or any leaked source.
