import { router } from 'expo-router';
export default function Home() { return <button onClick={() => router.push('/details')}>Details</button>; }
