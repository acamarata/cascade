/**
 * Purpose: Class name utility — merges Tailwind classes with clsx and tailwind-merge.
 * Inputs:  ClassValue[] — any mix of strings, arrays, objects
 * Outputs: string — merged, deduplicated class string
 * Constraints: Used by every shadcn/ui component; must be tree-shakeable.
 * SPORT: MASTER-UTILS.md — cn
 */

import { type ClassValue, clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
