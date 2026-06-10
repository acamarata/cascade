import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/**
 * Merge Tailwind class names, resolving conflicts so later utilities win.
 *
 * @param inputs - Class values (strings, arrays, conditional objects).
 * @returns A single deduplicated class string.
 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs))
}
