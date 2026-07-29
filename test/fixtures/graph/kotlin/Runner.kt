package fixture

interface Worker {
  fun run()
}

class Runner : Worker {
  override fun run() {}
}
