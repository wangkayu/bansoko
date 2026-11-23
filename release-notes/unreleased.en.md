## New

- Replacement comparison adds a file size dimension (file.size) and an unknown option for subtitle flag (sub_flag), both configurable in preference order.

## Improved

- Redesigned the version comparison editor so dimensions and value priorities can be added, reordered, and edited directly.
- Refined the four tabs of the anime configuration dialog (Metadata, Subgroups, Rules, and RSS): grouped metadata fields with confirmation before unbinding TMDB and inline validation for numeric inputs; unified the subgroup toolbar with unsaved-edit protection on refresh; added collapsible rule cards and live regex validation; kept the Show Filtered toggle enabled during RSS preview and unified draft/owner preview flows.

## Fixed

- Prevented adding duplicate Jackett connections after one is already configured, enforcing a single Jackett connection in both the server and the New Connection dialog while keeping the existing connection editable.
