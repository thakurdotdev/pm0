"use client";

import Link from "next/link";
import { Menu } from "lucide-react";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { LogoWordmark } from "./logo";
import { ThemeToggle } from "./theme-toggle";

const LINKS = [
  { href: "/#features", label: "Features" },
  { href: "/#benchmarks", label: "Benchmarks" },
  { href: "/docs", label: "Docs" },
  { href: "/changelog", label: "Changelog" },
];

export function MobileNav() {
  return (
    <Sheet>
      <SheetTrigger
        aria-label="Open navigation menu"
        className="inline-flex size-9 items-center justify-center rounded-lg border border-line text-ink-muted hover:border-line-strong hover:text-ink md:hidden"
      >
        <Menu className="size-4" aria-hidden="true" />
      </SheetTrigger>
      <SheetContent
        side="right"
        className="flex w-72 flex-col gap-6 border-line bg-background p-6"
      >
        <SheetHeader className="p-0">
          <SheetTitle asChild>
            <Link href="/" aria-label="pm0 home">
              <LogoWordmark />
            </Link>
          </SheetTitle>
        </SheetHeader>
        <nav aria-label="Mobile navigation" className="flex flex-col">
          {LINKS.map((link) => (
            <Link
              key={link.href}
              href={link.href}
              className="rounded-lg py-2.5 text-[0.9375rem] text-ink-muted transition-colors hover:text-ink"
            >
              {link.label}
            </Link>
          ))}
        </nav>
        <div className="flex items-center justify-between border-t border-line pt-4">
          <span className="label-caption">Theme</span>
          <ThemeToggle />
        </div>
      </SheetContent>
    </Sheet>
  );
}
