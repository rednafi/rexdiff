-- "rexdiff install" wrote this file. To remove it, run "rexdiff uninstall".
--
-- These are your settings of rexdiff. The user guide lists them:
-- https://github.com/rednafi/rexdiff/blob/master/.doc/user.md#settings
-- To change a setting, edit it, then run "rex config reload".
-- rexdiff does not change this file. The action is in rexdiff_toggle.lua.
local settings = {
  -- key = "shift+cmd+r", -- the key of the toggle, or false for none
  -- placement = "split", -- split, zoomed, tab, or overlay
  -- direction = "right", -- of the split: right or down
  -- layout = "unified", -- or split
}

-- rexdiff_toggle.lua has the toggle action, and rexdiff writes it. If it
-- fails, the rest of Rex's config still loads, and the key shows why.
local ok, err = pcall(function() require("rexdiff_toggle")(settings) end)
if not ok then
  rex.action{ name = "rexdiff_toggle", title = "Toggle rexdiff",
    run = function() error("rexdiff: " .. tostring(err), 0) end }
  local key = type(settings) == "table" and settings.key
  if key == nil then key = "shift+cmd+r" end
  if key then rex.bind(key, "rexdiff_toggle") end
end
