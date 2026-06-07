#!/usr/bin/env bash
#
# gen-sample-library.sh — generate a small sample library for testing all three
# gofin library kinds in one go. Three subfolders are created under <outdir>:
#
#   <outdir>/tv/      — Series / Season / Episode .webm files (VP9 + Opus)
#   <outdir>/movies/  — flat movie .webm files (VP9 + Opus)
#   <outdir>/music/   — Artist / Album / Track .opus files with tags
#
# Output uses royalty-free codecs (VP9 video, Opus audio) so the files direct
# play in every modern browser, including Chromium builds without H.264/AAC.
#
# Usage:
#   scripts/gen-sample-library.sh [outdir]
#
# Defaults: outdir=./sample
#
# Requires: ffmpeg with libvpx-vp9, libopus, and the lavfi testsrc/sine sources.

set -euo pipefail

OUTDIR="${1:-./sample}"

DURATION=30
WIDTH=854
HEIGHT=480
FPS=30

if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "error: ffmpeg not found on PATH" >&2
  exit 1
fi

# drawtext (libfreetype) + a real font file are optional; we fall back to a
# plain test pattern when either is missing.
FONT=""
if ffmpeg -hide_banner -filters 2>/dev/null | grep -q ' drawtext '; then
  for f in \
    /usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf \
    /usr/share/fonts/truetype/dejavu/DejaVuSans.ttf \
    /usr/share/fonts/TTF/DejaVuSans.ttf \
    /System/Library/Fonts/Supplemental/Arial.ttf; do
    if [[ -f "$f" ]]; then
      FONT="$f"
      break
    fi
  done
fi

gen_video() {
  # gen_video <label> <outfile> <audio-freq>
  local label="$1" outfile="$2" freq="$3"
  local vf="testsrc=size=${WIDTH}x${HEIGHT}:rate=${FPS}"
  if [[ -n "$FONT" ]]; then
    # Escape colons and backslashes inside the label for drawtext's parser.
    local txt="${label//\\/\\\\}"
    txt="${txt//:/\\:}"
    vf="${vf},drawtext=fontfile=${FONT}:text='${txt}':fontcolor=white:fontsize=48:box=1:boxcolor=black@0.5:boxborderw=12:x=(w-text_w)/2:y=(h-text_h)/2"
  fi
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "${vf}" \
    -f lavfi -i "sine=frequency=${freq}:sample_rate=48000" \
    -t "$DURATION" \
    -c:v libvpx-vp9 -b:v 300k -deadline realtime -cpu-used 8 -pix_fmt yuv420p \
    -c:a libopus -b:a 64k \
    -metadata title="${label}" \
    "$outfile"
}

gen_audio() {
  # gen_audio <title> <album> <artist> <track-num> <outfile> <freq>
  local title="$1" album="$2" artist="$3" track="$4" outfile="$5" freq="$6"
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "sine=frequency=${freq}:sample_rate=48000:duration=${DURATION}" \
    -c:a libopus -b:a 64k \
    -metadata title="${title}" \
    -metadata album="${album}" \
    -metadata artist="${artist}" \
    -metadata album_artist="${artist}" \
    -metadata track="${track}" \
    "$outfile"
}

echo "Generating sample library in: ${OUTDIR}"
[[ -z "$FONT" ]] && echo "  note: drawtext filter or font unavailable; videos use a plain test pattern"

# --- TV ---
TV_SERIES=("Test Show" "Demo Series")
TV_SEASONS=1
TV_EPISODES=2
echo "tv/"
for series in "${TV_SERIES[@]}"; do
  for ((s = 1; s <= TV_SEASONS; s++)); do
    season_dir="${OUTDIR}/tv/${series}/Season $(printf '%02d' "$s")"
    mkdir -p "$season_dir"
    for ((e = 1; e <= TV_EPISODES; e++)); do
      tag="S$(printf '%02d' "$s")E$(printf '%02d' "$e")"
      outfile="${season_dir}/${series} ${tag}.webm"
      freq=$(( 220 + (s * 100) + (e * 30) ))
      echo "  -> ${outfile}"
      gen_video "${series} ${tag}" "$outfile" "$freq"
    done
  done
done

# --- Movies ---
MOVIES=(
  "Big Buck Bunny (2008)"
  "Sintel (2010)"
  "Tears of Steel (2012)"
)
echo "movies/"
mkdir -p "${OUTDIR}/movies"
freq=300
for label in "${MOVIES[@]}"; do
  outfile="${OUTDIR}/movies/${label}.webm"
  echo "  -> ${outfile}"
  gen_video "$label" "$outfile" "$freq"
  freq=$((freq + 110))
done

# --- Music ---
# Two artists, each with one album, three tracks. Layout matches the music
# scanner's path-fallback (Artist/Album/Track), and tags are embedded so the
# scanner reads them via dhowden/tag.
declare -a TRACKS=(
  "Echo Hill|First Light|01|Sunrise|440"
  "Echo Hill|First Light|02|Noon Tide|494"
  "Echo Hill|First Light|03|Dusk Bell|554"
  "Nova Choir|Open Sky|01|Cirrus|330"
  "Nova Choir|Open Sky|02|Stratus|370"
  "Nova Choir|Open Sky|03|Nimbus|415"
)
echo "music/"
for entry in "${TRACKS[@]}"; do
  IFS='|' read -r artist album track title freq <<<"$entry"
  album_dir="${OUTDIR}/music/${artist}/${album}"
  mkdir -p "$album_dir"
  outfile="${album_dir}/${track} - ${title}.opus"
  echo "  -> ${outfile}"
  gen_audio "$title" "$album" "$artist" "$track" "$outfile" "$freq"
done

echo "Done."
echo
echo "Point gofin.yaml at the generated folders, e.g.:"
echo "  libraries:"
echo "    - { name: TV Shows, type: tvshows, path: ${OUTDIR}/tv }"
echo "    - { name: Movies,   type: movies,  path: ${OUTDIR}/movies }"
echo "    - { name: Music,    type: music,   path: ${OUTDIR}/music }"
