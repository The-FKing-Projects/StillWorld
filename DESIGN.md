# Interface design

The initial server-rendered interface is a small, shared shell for game-owned
text and actions. It is intended as an integration example, not a game brand.

- Use system fonts, a light background, a dark green header, and high-contrast
  controls. Keep the content column readable and adapt it to narrow screens.
- Use semantic headings, labels, lists, and ordinary forms. Provide visible
  keyboard focus and status text that does not depend on color.
- Show the player's current Turns, shared game state, and action costs before
  presenting action buttons. Render game-provided text as escaped text.
- Present committed results separately from current state. Preserve the
  original form data when offering a retry for an uncertain outcome.
