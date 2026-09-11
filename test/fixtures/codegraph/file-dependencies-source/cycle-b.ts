import { cycleA } from './cycle-a';

export function cycleB(): number {
  return cycleA();
}
