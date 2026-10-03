"use client";

import { useEffect, useRef, useState } from "react";

/**
 * Scroll-reveal: opacity 0→1 + translateY 8px→0, 300ms, landing only.
 * prefers-reduced-motion is handled in globals.css (element renders visible).
 */
export function Reveal({
  children,
  className,
  delay = 0,
}: {
  children: React.ReactNode;
  className?: string;
  delay?: number;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    // Safety net: never leave content invisible (full-page captures, odd
    // IntersectionObserver failures). IO still reveals on real scroll first.
    const fallback = setTimeout(() => setVisible(true), 2500);
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            setVisible(true);
            observer.disconnect();
            clearTimeout(fallback);
          }
        }
      },
      { threshold: 0.12 },
    );
    observer.observe(el);
    return () => {
      observer.disconnect();
      clearTimeout(fallback);
    };
  }, []);

  return (
    <div
      ref={ref}
      style={delay ? { transitionDelay: `${delay}ms` } : undefined}
      className={`reveal ${visible ? "reveal-visible" : ""} ${className ?? ""}`}
    >
      {children}
    </div>
  );
}
