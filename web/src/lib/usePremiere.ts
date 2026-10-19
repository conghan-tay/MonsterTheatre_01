"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { audioElement } from "./audio";
import type { Clip, Premiere } from "./theatre";

// Where the premiere is:
//   countdown  its start time is ahead
//   ready      its start time has passed and it has not been played here:
//              a finished room, opened later (docs/decisions.md, D039)
//   playing    the recordings are playing, one after another
//   blocked    the browser refused to play without a click
//   finished   all of them have played to the end
//   error      a recording could not be loaded or played
export type PremiereStatus = "countdown" | "ready" | "playing" | "blocked" | "finished" | "error";

// usePremiere counts down to the premiere and plays its clips in order.
//
// The clips are fetched whole as soon as the premiere is known, during the
// countdown, and played from those copies, so each is ready before the one
// before it ends. If one is still loading when its turn comes, playback
// waits for it; nothing is skipped.
export function usePremiere(premiere: Premiere | undefined) {
  const [status, setStatus] = useState<PremiereStatus | null>(null);
  const [secondsLeft, setSecondsLeft] = useState(0);
  // The clip playing now, and how many have played to the end.
  const [current, setCurrent] = useState<Clip | null>(null);
  // The playing clip's lip-sync video, as a local URL, if it has one that
  // loaded (D056).
  const [video, setVideo] = useState<string | null>(null);
  const [played, setPlayed] = useState(0);

  const clips = useRef<Clip[]>([]);
  const loaded = useRef<Promise<string>[]>([]);
  // Each clip's video, fetched whole like its recording; null where there
  // is none or it could not be fetched, and the portrait is shown instead.
  const videos = useRef<Promise<string | null>[]>([]);
  // Each start of playback gets a number; a run that is no longer the
  // latest stops at its next step.
  const run = useRef(0);

  const play = useCallback(async () => {
    const mine = ++run.current;
    const audio = audioElement();
    setPlayed(0);
    setStatus("playing");
    try {
      for (const [i, clip] of clips.current.entries()) {
        const src = await loaded.current[i];
        const videoSrc = await videos.current[i];
        if (mine !== run.current) return;
        setCurrent(clip);
        setVideo(videoSrc);
        audio.src = src;
        const ended = new Promise<void>((resolve, reject) => {
          audio.onended = () => resolve();
          audio.onerror = () => reject(new Error("the recording could not be played"));
        });
        await audio.play();
        await ended;
        if (mine !== run.current) return;
        setPlayed(i + 1);
      }
      setCurrent(null);
      setVideo(null);
      setStatus("finished");
    } catch (err) {
      if (mine !== run.current) return;
      setCurrent(null);
      setVideo(null);
      // NotAllowedError is the browser's autoplay rule: no click yet.
      setStatus(err instanceof DOMException && err.name === "NotAllowedError" ? "blocked" : "error");
    }
  }, []);

  // One premiere is one start time and one list of clips. A snapshot that
  // repeats them, after a resync or a reconnection, changes nothing here.
  const startsAt = premiere?.startsAt;
  const clipList = premiere ? JSON.stringify(premiere.clips) : undefined;
  useEffect(() => {
    if (!startsAt || !clipList) return;
    const list: Clip[] = JSON.parse(clipList);
    clips.current = list;
    const urls: string[] = [];
    loaded.current = list.map(async (clip) => {
      const response = await fetch(clip.url, { credentials: "same-origin" });
      if (!response.ok) throw new Error(`recording ${clip.turn} answered ${response.status}`);
      const url = URL.createObjectURL(await response.blob());
      urls.push(url);
      return url;
    });
    // A failed load is reported when its clip's turn comes.
    loaded.current.forEach((p) => p.catch(() => undefined));
    videos.current = list.map(async (clip) => {
      if (!clip.videoUrl) return null;
      try {
        const response = await fetch(clip.videoUrl, { credentials: "same-origin" });
        if (!response.ok) return null;
        const url = URL.createObjectURL(await response.blob());
        urls.push(url);
        return url;
      } catch {
        return null;
      }
    });

    const start = Date.parse(startsAt);
    const tick = () => {
      const left = start - Date.now();
      if (left > 0) {
        setStatus("countdown");
        setSecondsLeft(Math.ceil(left / 1000));
        return false;
      }
      return true;
    };
    let timer: ReturnType<typeof setInterval> | undefined;
    // Decided a moment after mounting, not during it, so that this effect
    // only ever sets state from a timer.
    const first = setTimeout(() => {
      if (tick()) {
        setStatus("ready");
        return;
      }
      timer = setInterval(() => {
        if (!tick()) return;
        clearInterval(timer);
        void play();
      }, 200);
    }, 0);

    return () => {
      clearTimeout(first);
      clearInterval(timer);
      run.current += 1;
      const audio = audioElement();
      audio.pause();
      audio.onended = audio.onerror = null;
      urls.forEach((url) => URL.revokeObjectURL(url));
    };
  }, [startsAt, clipList, play]);

  return { status: premiere ? status : null, secondsLeft, current, video, played, play };
}
