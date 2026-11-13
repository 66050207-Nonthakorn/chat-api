resource "aws_vpc" "chat" {
  cidr_block           = "10.42.0.0/16"
  enable_dns_hostnames = true
  enable_dns_support   = true
  tags                 = { Name = "cloudchat-demo" }
}
resource "aws_internet_gateway" "chat" { vpc_id = aws_vpc.chat.id }
resource "aws_subnet" "public" {
  count                   = 2
  vpc_id                  = aws_vpc.chat.id
  cidr_block              = cidrsubnet(aws_vpc.chat.cidr_block, 8, count.index)
  availability_zone       = data.aws_availability_zones.available.names[count.index]
  map_public_ip_on_launch = false
}
resource "aws_subnet" "private" {
  count             = 2
  vpc_id            = aws_vpc.chat.id
  cidr_block        = cidrsubnet(aws_vpc.chat.cidr_block, 8, count.index + 10)
  availability_zone = data.aws_availability_zones.available.names[count.index]
}
resource "aws_subnet" "data" {
  count             = 2
  vpc_id            = aws_vpc.chat.id
  cidr_block        = cidrsubnet(aws_vpc.chat.cidr_block, 8, count.index + 20)
  availability_zone = data.aws_availability_zones.available.names[count.index]
}
resource "aws_eip" "nat" { domain = "vpc" }
resource "aws_nat_gateway" "chat" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public[0].id
  depends_on    = [aws_internet_gateway.chat]
}
resource "aws_route_table" "public" { vpc_id = aws_vpc.chat.id }
resource "aws_route" "internet" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.chat.id
}
resource "aws_route_table_association" "public" {
  count          = 2
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}
resource "aws_route_table" "private" { vpc_id = aws_vpc.chat.id }
resource "aws_route" "nat" {
  route_table_id         = aws_route_table.private.id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.chat.id
}
resource "aws_route_table_association" "private" {
  count          = 2
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private.id
}
resource "aws_route_table" "data" { vpc_id = aws_vpc.chat.id }
resource "aws_route_table_association" "data" {
  count          = 2
  subnet_id      = aws_subnet.data[count.index].id
  route_table_id = aws_route_table.data.id
}
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.chat.id
  service_name      = "com.amazonaws.${var.aws_region}.s3"
  route_table_ids   = [aws_route_table.private.id]
  vpc_endpoint_type = "Gateway"
}
