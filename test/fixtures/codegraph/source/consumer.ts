import { run } from './main';
export function processGreeting(enabled: boolean): string {
  if (enabled) {
    return run();
  }
  return 'skipped';
}
