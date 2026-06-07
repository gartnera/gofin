#!/usr/bin/env bash
#
# gen-sample-tv.sh — generate a small library of sample TV show videos for
# testing gofin's scanner. Each episode is a 480p, 30-second clip with a
# burned-in label (series / season / episode) and a distinct audio tone so the
# files are easy to tell apart while testing playback.
#
# Output is webm (VP9 video + Opus audio) — royalty-free codecs that direct
# play in every modern browser, including Chromium builds without H.264/AAC.
#
# The output is laid out the way gofin's tvshows scanner expects:
#
#   <outdir>/
#     <Series Name>/
#       Season 01/
#         <Series Name> S01E01.webm
#         <Series Name> S01E02.webm
#       Season 02/
#         ...
#
# Usage:
#   scripts/gen-sample-tv.sh [outdir] [seasons] [episodes-per-season]
#
# Defaults: outdir=./sample-tv, seasons=2, episodes-per-season=3
#
# Requires: ffmpeg (with libvpx-vp9, libopus, and the lavfi testsrc/sine
# sources).

set -euo pipefail

OUTDIR="${1:-./sample-tv}"
SEASONS="${2:-2}"
EPISODES="${3:-3}"

# Duration / resolution / fps of each generated clip.
DURATION=30
WIDTH=854   # 854x480 = 480p at 16:9
HEIGHT=480
FPS=30

# A couple of fictional series so the library has more than one entry.
SERIES=("Test Show" "Demo Series")

if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "error: ffmpeg not found on PATH" >&2
  exit 1
fi

# The label overlay needs the drawtext filter (ffmpeg built with libfreetype)
# AND a usable font file. Many minimal builds (e.g. some Homebrew ffmpeg builds
# on macOS) omit drawtext, so check for the filter before relying on it. If
# either is missing we fall back to a plain test pattern.
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

gen_episode() {
  local series="$1" season="$2" episode="$3" outfile="$4"

  # Give each episode a different base tone so playback is distinguishable.
  local freq=$(( 220 + (season * 100) + (episode * 30) ))

  local tag="S$(printf '%02d' "$season")E$(printf '%02d' "$episode")"
  local vf="testsrc=size=${WIDTH}x${HEIGHT}:rate=${FPS}"
  if [[ -n "$FONT" ]]; then
    vf="${vf},drawtext=fontfile=${FONT}:text='${series}\n${tag}':fontcolor=white:fontsize=48:box=1:boxcolor=black@0.5:boxborderw=12:x=(w-text_w)/2:y=(h-text_h)/2"
  fi

  # Also embed the label as the container title so episodes stay identifiable
  # even when drawtext isn't available and the video is a plain test pattern.
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "${vf}" \
    -f lavfi -i "sine=frequency=${freq}:sample_rate=48000" \
    -t "$DURATION" \
    -c:v libvpx-vp9 -b:v 300k -deadline realtime -cpu-used 8 -pix_fmt yuv420p \
    -c:a libopus -b:a 64k \
    -metadata title="${series} ${tag}" \
    "$outfile"
}

echo "Generating sample TV shows in: ${OUTDIR}"
echo "  series=${#SERIES[@]} seasons=${SEASONS} episodes/season=${EPISODES}"
echo "  clip: ${WIDTH}x${HEIGHT} @ ${FPS}fps, ${DURATION}s each"
[[ -z "$FONT" ]] && echo "  note: drawtext filter or font unavailable; plain test pattern (label still set as metadata title)"

for series in "${SERIES[@]}"; do
  for ((s = 1; s <= SEASONS; s++)); do
    season_dir="${OUTDIR}/${series}/Season $(printf '%02d' "$s")"
    mkdir -p "$season_dir"
    for ((e = 1; e <= EPISODES; e++)); do
      outfile="${season_dir}/${series} S$(printf '%02d' "$s")E$(printf '%02d' "$e").webm"
      echo "  -> ${outfile}"
      gen_episode "$series" "$s" "$e" "$outfile"
    done
  done
done

echo "Done."
