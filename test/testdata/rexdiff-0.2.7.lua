-- Installed by "rexdiff install". Remove with "rexdiff uninstall".
--
-- The settings of rexdiff. Edit them, then run "rex config reload". The
-- README of rexdiff lists them. Install and upgrades keep this part.
local settings = {
  -- key = "shift+cmd+r", -- the key of the toggle, or false for none
  -- placement = "split", -- split, zoomed, tab, or overlay
  -- layout = "split",
}

-- rexdiff code below. Install and upgrades replace it. Do not edit this line.
local BIN = "{{BIN}}"
local PLACEMENTS = { split = true, zoomed = true, tab = true, overlay = true }
local TERM = "com.superlogical.terminal"

-- must returns res, or fails the action with err. A Rex call returns nil
-- and an error message when it fails.
local function must(res, err)
  if err ~= nil then error(err, 0) end
  return res
end

-- call calls method on the terminal block with the ID id.
local function call(method, id)
  return must(rex.block.call(TERM, method, { block_id = id }))
end

-- runs_rexdiff returns true if the terminal block id runs rexdiff. rexdiff
-- reports app=rexdiff to Rex with OSC 7501. Rex keeps an idle status after
-- its program stops, so the block counts only while the process that
-- reported the status is in the foreground.
local function runs_rexdiff(id)
  for _, r in ipairs(call("program_status", id).records or {}) do
    if r.app == "rexdiff" and r.owner then
      local fg = call("process", id).foreground
      return fg ~= nil and fg.pid == r.owner.pid
    end
  end
  return false
end

-- reviewers returns the IDs of the blocks in the session that run rexdiff.
local function reviewers()
  local ids = {}
  for _, b in ipairs(must(rex.session.list_blocks{}).blocks or {}) do
    if b.creator_name == TERM and runs_rexdiff(b.block_id) then
      table.insert(ids, b.block_id)
    end
  end
  return ids
end

-- check fails the action if rexdiff does not accept config, the settings
-- as a JSON object. Here the user sees the error. If rexdiff stops at once
-- in a new pane, the pane closes and the error is lost.
local function check(config)
  local r, err = rex.exec({ BIN, "--check", "--config", config })
  if r == nil then error(err, 0) end
  if not r.ok then error((r.stderr:gsub("%s+$", "")), 0) end
end

-- open_rexdiff opens rexdiff at placement, beside the agent in ctx, with
-- the settings in config. rexdiff starts in the current directory of the
-- agent. The rex package of rexdiff finds the directory the same way.
local function open_rexdiff(ctx, placement, config)
  local p = call("process", ctx.block_id)
  local cwd = (p.foreground or {}).cwd or (p.child or {}).cwd
  local node = rex.layout.block{
    flavor = TERM .. ".shell",
    label = "rexdiff",
    options = {
      command = { BIN, "--agent", ctx.block_id, "--config", config },
      cwd = cwd,
      shell = "none",
    },
  }
  if placement == "tab" then
    return must(rex.session.new_window{ window_label = "rexdiff", layout = node })
  elseif placement == "overlay" then
    return must(rex.session.new_layer{
      bounds = { 0.04, 0.04, 0.92, 0.92 },
      layout = node,
      focus = true,
    })
  end
  local s = must(rex.session.new_split{
    anchor_id = ctx.block_id,
    direction = "horizontal",
    side = "after",
    ratio = 0.45,
    focus = true,
    layout = node,
  })
  if placement == "zoomed" then
    must(rex.session.zoom_block{ block_id = s.block_ids[1], zoom = true })
  end
  return s
end

-- The key and the placement are for Rex. rexdiff gets the other settings
-- as JSON.
if type(settings) ~= "table" then
  error("rexdiff.lua: settings must be a table, as in: local settings = { }", 0)
end
local opts = {}
for k, v in pairs(settings) do opts[k] = v end
local key = opts.key
if key == nil then key = "shift+cmd+r" end
local default_placement = opts.placement or "split"
opts.key, opts.placement = nil, nil
local config, encode_err = rex.json.encode(opts)

rex.action{
  name = "rexdiff_toggle",
  title = "Toggle rexdiff",
  description = "Review the agent's changes beside it and send comments back",
  category = "rexdiff",
  keywords = "diff review git comments agent",
  args = { placement = "string?" }, -- split, zoomed, tab, or overlay
  run = function(ctx, args)
    local placement = args.placement or default_placement
    if not PLACEMENTS[placement] then
      error("unknown placement " .. tostring(placement)
        .. "; use split, zoomed, tab, or overlay", 0)
    end
    local open = reviewers()
    if #open == 0 then
      if config == nil then error("rexdiff settings: " .. encode_err, 0) end
      check(config)
      return open_rexdiff(ctx, placement, config)
    end
    for _, id in ipairs(open) do
      must(rex.call("block.close", { session_id = ctx.session_id, block_id = id }))
    end
    return { closed = open }
  end,
}
if key then rex.bind(key, "rexdiff_toggle") end
