import { cycleB } from './cycle-b';

export function cycleA(): number {
  return cycleB();
}
