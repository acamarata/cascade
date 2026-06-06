/**
 * Purpose: Re-exports shadcn Skeleton with aria-busy=true for accessible loading states.
 * Inputs:  Standard HTMLDivElement props + cn className merging
 * Outputs: A pulsing skeleton placeholder indicating loading content.
 * Constraints: aria-busy=true signals to assistive tech that content is loading.
 *              Delegates all visual styling to shadcn skeleton class.
 * SPORT: MASTER-COMPONENTS.md — Skeleton
 */

import React from "react";
import { cn } from "@/lib/utils";

interface SkeletonProps extends React.HTMLAttributes<HTMLDivElement> {
  /** Extra Tailwind classes for sizing/shape. */
  className?: string;
}

/**
 * Skeleton placeholder with built-in aria-busy=true.
 * Use inside loading states to indicate content is being fetched.
 */
export function Skeleton({ className, ...props }: SkeletonProps) {
  return (
    <div
      aria-busy={true}
      className={cn("animate-pulse rounded-md bg-muted", className)}
      {...props}
    />
  );
}
