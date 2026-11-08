--[[
Booky read-along: changes how fast the narration plays without changing
its pitch. Raw PCM (s16le, mono) comes in on stdin and goes out on stdout
at `speed` times the pace:

    luajit booky_stretch.lua <speed> <rate>

It is WSOLA (waveform-similarity overlap-add): the output is built from
short overlapping frames of the input, taken `speed` times further apart
than they're laid down. Each frame is nudged, within a few milliseconds,
to where it best continues the frame before it, so the voice's waveform
joins up instead of warbling.

Run by KOReader's own LuaJIT, in the playback pipeline between the chunk
files and the audio sink. Needs LuaJIT's ffi.
]]

local ffi = require("ffi")

local M = {}

-- Stretcher state for one stream. rate: samples per second.
function M.new(speed, rate)
    local N = math.floor(0.030 * rate + 0.5) -- frame: 30 ms
    if N % 2 == 1 then N = N + 1 end
    local Hs = N / 2 -- output hop: frames overlap by half
    local s = {
        speed = speed,
        N = N,
        Hs = Hs,
        Ha = Hs * speed, -- input hop
        D = math.floor(0.008 * rate + 0.5), -- search: ±8 ms
        win = ffi.new("double[?]", N),
        -- input kept in a growing window: buf holds samples from absolute
        -- index base onwards
        buf = ffi.new("double[?]", 1),
        cap = 0,
        base = 0,
        len = 0,
        acc = ffi.new("double[?]", N), -- output being overlap-added
        k = 0, -- next output frame
        prev = nil, -- where the previous frame was taken from (absolute)
    }
    for n = 0, N - 1 do
        s.win[n] = 0.5 - 0.5 * math.cos(2 * math.pi * n / N) -- periodic Hann: halves sum to 1
    end
    return s
end

-- push appends n samples from src (an int16_t array).
local function push(s, src, n)
    if s.len + n > s.cap then
        local cap = math.max(2 * s.cap, s.len + n, 4 * s.N)
        local nb = ffi.new("double[?]", cap)
        ffi.copy(nb, s.buf, s.len * ffi.sizeof("double"))
        s.buf, s.cap = nb, cap
    end
    for i = 0, n - 1 do
        s.buf[s.len + i] = src[i]
    end
    s.len = s.len + n
end

-- drop forgets input before absolute index upto.
local function drop(s, upto)
    local cut = upto - s.base
    if cut <= 0 then return end
    if cut > s.len then cut = s.len end
    ffi.C.memmove(s.buf, s.buf + cut, (s.len - cut) * ffi.sizeof("double"))
    s.len = s.len - cut
    s.base = s.base + cut
end

ffi.cdef("void *memmove(void *dest, const void *src, size_t n);")

-- best returns the start (absolute) near ideal where a frame best
-- continues the previous one: the highest normalised correlation between
-- the frame's first half and what naturally followed the previous frame.
local function best(s, ideal)
    if not s.prev then return ideal end
    local buf, base, Hs = s.buf, s.base, s.Hs
    local nat = s.prev + Hs - base -- the natural continuation, in buf
    local lo, hi = math.max(ideal - s.D, s.base), ideal + s.D
    local bestAt, bestScore = ideal, -math.huge
    for c = lo, hi do
        local at = c - base
        local xy, yy = 0, 1e-9
        for i = 0, Hs - 1, 2 do -- every other sample: plenty for speech
            local y = buf[at + i]
            xy = xy + buf[nat + i] * y
            yy = yy + y * y
        end
        local score = xy / math.sqrt(yy)
        if score > bestScore then bestAt, bestScore = c, score end
    end
    return bestAt
end

-- frames lays down every output frame the buffered input allows (all of
-- them when final), calling out(int16_t array, n) with finished samples.
local function frames(s, out, final, tmp)
    local N, Hs = s.N, s.Hs
    while true do
        local ideal = math.floor(s.k * s.Ha + 0.5)
        local need = ideal + s.D + N -- the furthest sample a frame may use
        if s.prev then need = math.max(need, s.prev + Hs + Hs) end
        if need > s.base + s.len then
            if not final or ideal >= s.base + s.len then break end
        end
        local at = best(s, ideal)
        local from = at - s.base
        local avail = s.len - from
        for n = 0, N - 1 do
            local x = n < avail and s.buf[from + n] or 0
            s.acc[n] = s.acc[n] + s.win[n] * x
        end
        -- the first half is finished: write it, slide the rest down
        for n = 0, Hs - 1 do
            local v = s.acc[n]
            if v > 32767 then v = 32767 elseif v < -32768 then v = -32768 end
            tmp[n] = v
        end
        out(tmp, Hs)
        for n = 0, Hs - 1 do
            s.acc[n] = s.acc[n + Hs]
            s.acc[n + Hs] = 0
        end
        s.prev = at
        s.k = s.k + 1
        -- keep what the next search may still look at
        drop(s, math.min(math.floor(s.k * s.Ha + 0.5) - s.D, s.prev + Hs))
    end
    if final then
        for n = 0, Hs - 1 do
            local v = s.acc[n]
            if v > 32767 then v = 32767 elseif v < -32768 then v = -32768 end
            tmp[n] = v
        end
        out(tmp, Hs)
    end
end

-- run stretches all of `read` (returns byte strings, nil at the end) to
-- `write` (takes byte strings).
function M.run(speed, rate, read, write)
    if speed == 1 then
        for data in read do write(data) end
        return
    end
    local s = M.new(speed, rate)
    local tmp = ffi.new("int16_t[?]", s.N)
    local out = function(arr, n) write(ffi.string(arr, n * 2)) end
    local carry = ""
    for data in read do
        data = carry .. data
        local n = math.floor(#data / 2)
        carry = data:sub(2 * n + 1)
        if n > 0 then
            local samples = ffi.new("int16_t[?]", n)
            ffi.copy(samples, data, 2 * n)
            push(s, samples, n)
            frames(s, out, false, tmp)
        end
    end
    frames(s, out, true, tmp)
end

-- as a program: stdin to stdout
if arg and arg[0] and arg[0]:match("booky_stretch%.lua$") then
    local speed, rate = tonumber(arg[1]), tonumber(arg[2])
    if not speed or speed < 0.5 or speed > 2 or not rate or rate < 8000 or rate > 48000 then
        io.stderr:write("usage: booky_stretch.lua <speed 0.5-2> <rate>\n")
        os.exit(2)
    end
    local stdin, stdout = io.stdin, io.stdout
    stdout:setvbuf("full", 65536)
    M.run(speed, rate, function() return stdin:read(16384) end, function(b)
        stdout:write(b)
    end)
    stdout:flush()
end

return M
