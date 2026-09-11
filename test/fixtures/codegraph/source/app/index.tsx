import { router } from 'expo-router';
export function openDetails(enabled: boolean) {
  if (enabled) {
    router.push('/details');
  }
}
export default function Home() { return <button onClick={() => openDetails(true)}>Details</button>; }
